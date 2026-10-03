package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"procurementcore/internal/amazon"
	"procurementcore/internal/auth"
	"procurementcore/internal/models"
	"procurementcore/internal/service"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) amazonConfig(w http.ResponseWriter, _ *http.Request) {
	if h.amazon == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "mode": h.amazon.Mode(), "readyToOrder": h.amazon.ReadyToOrder(), "shipTo": h.amazon.ShipTo()})
}

func (h *Handler) startAmazonPunchout(w http.ResponseWriter, r *http.Request) {
	if h.amazon == nil {
		badRequest(w, "Amazon PunchOut ist noch nicht eingerichtet")
		return
	}
	user := auth.CurrentUser(r)
	buyerEmail, err := h.amazonBuyerEmail(user.ID)
	if err != nil || buyerEmail == "" {
		badRequest(w, "Für dein Benutzerkonto fehlt eine E-Mail-Adresse")
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		serverError(w, err)
		return
	}
	cookie := hex.EncodeToString(random[:])
	session := models.AmazonPunchoutSession{TokenHash: tokenHash(cookie), UserID: user.ID, Username: user.Username, BuyerEmail: buyerEmail, Status: "started", ExpiresAt: time.Now().Add(2 * time.Hour)}
	if err := h.db.Create(&session).Error; err != nil {
		serverError(w, err)
		return
	}
	startURL, err := h.amazon.Start(r.Context(), buyerEmail, cookie)
	if err != nil {
		_ = h.db.Model(&session).Update("status", "failed").Error
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": startURL})
}

func (h *Handler) amazonBuyerEmail(userID uint) (string, error) {
	var buyer struct{ Email string }
	err := h.db.Raw("SELECT email FROM users WHERE userid = ?", userID).Scan(&buyer).Error
	return buyer.Email, err
}

// HandleAmazonReturn is public because Amazon posts this form across sites.
// The random BuyerCookie is the only capability that can complete a session.
func (h *Handler) HandleAmazonReturn(w http.ResponseWriter, r *http.Request) {
	if h.amazon == nil {
		http.Error(w, "Amazon PunchOut ist nicht eingerichtet", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Amazon-Rückgabe", http.StatusBadRequest)
		return
	}
	var data []byte
	for name, values := range r.PostForm {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		switch {
		case strings.EqualFold(name, "cXML-urlencoded"):
			data = []byte(values[0])
		case strings.EqualFold(name, "cXML-base64") && len(data) == 0:
			var decodeErr error
			data, decodeErr = base64.StdEncoding.DecodeString(values[0])
			if decodeErr != nil {
				http.Error(w, "Ungültige Amazon-Rückgabe", http.StatusBadRequest)
				return
			}
		}
	}
	if len(data) == 0 {
		var err error
		data, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "Ungültige Amazon-Rückgabe", http.StatusBadRequest)
			return
		}
	}
	if len(data) == 0 {
		keys := make([]string, 0, len(r.PostForm))
		for key := range r.PostForm {
			keys = append(keys, key)
		}
		log.Printf("Amazon PunchOut callback without cXML: content-type=%q content-length=%d form-keys=%q", r.Header.Get("Content-Type"), r.ContentLength, keys)
	}
	cart, err := amazon.ParseCart(data)
	if err != nil {
		log.Printf("Amazon PunchOut callback rejected: cxml-bytes=%d error=%q", len(data), err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var reqID uint
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var session models.AmazonPunchoutSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ?", tokenHash(cart.BuyerCookie)).First(&session).Error; err != nil {
			return err
		}
		if session.Status != "started" || time.Now().After(session.ExpiresAt) {
			return fmt.Errorf("Amazon-Sitzung ist bereits verwendet oder abgelaufen")
		}
		var supplier models.Supplier
		if err := tx.Where("code = ?", "AMAZON-BUSINESS").First(&supplier).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			supplier = models.Supplier{Name: "Amazon Business", Code: "AMAZON-BUSINESS", Website: "https://www.amazon.de", Active: true}
			if err := tx.Create(&supplier).Error; err != nil {
				return err
			}
		}
		req := models.Requisition{AmazonPunchoutSessionID: &session.ID, Number: nextNumber("BAN"), Title: "Amazon Business Warenkorb", Status: "draft", RequesterID: session.UserID, RequesterName: session.Username, Justification: "Aus Amazon Business PunchOut übernommen"}
		for _, line := range cart.Lines {
			req.Lines = append(req.Lines, models.RequisitionLine{SupplierPartID: line.SupplierPartID, SupplierPartAuxiliaryID: line.SupplierPartAuxiliaryID, Description: line.Description, Quantity: float64(line.Quantity), Unit: line.Unit, EstimatedPriceCents: line.UnitPriceCents, PreferredSupplierID: &supplier.ID, PurchaseURL: line.URL})
		}
		req.EstimatedTotalCents = service.RequisitionTotal(req.Lines)
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		if err := tx.Model(&session).Update("status", "returned").Error; err != nil {
			return err
		}
		reqID = req.ID
		return tx.Create(&models.Activity{EntityType: "requisition", EntityID: req.ID, Action: "amazon_punchout_imported", UserID: session.UserID, Username: session.Username, Details: req.Number}).Error
	})
	if err != nil {
		http.Error(w, "Amazon-Warenkorb konnte nicht übernommen werden", http.StatusConflict)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	prefix := ""
	if strings.TrimSuffix(r.Header.Get("X-Forwarded-Prefix"), "/") == "/procurementcore" {
		prefix = "/procurementcore"
	}
	http.Redirect(w, r, fmt.Sprintf("%s/requisitions?amazon=imported&id=%d", prefix, reqID), http.StatusSeeOther)
}

func (h *Handler) submitAmazonOrder(w http.ResponseWriter, r *http.Request) {
	if isMCPMutation(r) {
		writeJSON(w, 428, map[string]string{"error": "Prepare the named supplier submission with exact final consent"})
		return
	}
	id, err := parseAmazonOrderID(r)
	if err != nil {
		badRequest(w, "Ungültige Bestellnummer")
		return
	}
	h.runAmazonSubmission(w, r, amazonSubmissionRequest{ID: int64(id)}, true)
}

func parseAmazonOrderID(r *http.Request) (uint, error) {
	value := strings.TrimSpace(chi.URLParam(r, "id"))
	var id uint
	_, err := fmt.Sscan(value, &id)
	if id == 0 {
		return 0, fmt.Errorf("invalid order id")
	}
	return id, err
}
