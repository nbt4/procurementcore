package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"procurementcore/internal/amazon"
	"procurementcore/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAmazonReturnCreatesOnePricedRequisition(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if dsn == "" || err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Skip("requires disposable PostgreSQL _test database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	const schema = "amazon_punchout_test"
	if err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Supplier{}, &models.Requisition{}, &models.RequisitionLine{}, &models.AmazonPunchoutSession{}, &models.Activity{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE users (userid BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO users(userid, email) VALUES (7, 'noah@example.com')").Error; err != nil {
		t.Fatal(err)
	}
	client, err := amazon.New(amazon.Config{FromIdentity: "buyer", SharedSecret: "private", TestURL: "http://localhost/test", LiveURL: "http://localhost/live", ReturnURL: "http://localhost/return", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, amazon: client}
	if email, err := h.amazonBuyerEmail(7); err != nil || email != "noah@example.com" {
		t.Fatalf("buyer email lookup: %q %v", email, err)
	}
	session := models.AmazonPunchoutSession{TokenHash: tokenHash("cookie"), UserID: 7, Username: "Noah", BuyerEmail: "noah@example.com", Status: "started", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	cart := `<cXML><Message><PunchOutOrderMessage><BuyerCookie>cookie</BuyerCookie><PunchOutOrderMessageHeader><Total><Money currency="EUR">24.68</Money></Total></PunchOutOrderMessageHeader><ItemIn quantity="2"><ItemID><SupplierPartID>B01</SupplierPartID><SupplierPartAuxiliaryID>opaque-token</SupplierPartAuxiliaryID></ItemID><ItemDetail><UnitPrice><Money currency="EUR">12.34</Money></UnitPrice><Description xml:lang="de">Adapter</Description><UnitOfMeasure>EA</UnitOfMeasure></ItemDetail></ItemIn></PunchOutOrderMessage></Message></cXML>`
	form := url.Values{"cXML-urlencoded": []string{cart}}.Encode()
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/amazon/punchout/return", strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.HandleAmazonReturn(w, r)
		return w
	}
	if w := call(); w.Code != http.StatusSeeOther {
		t.Fatalf("first callback: %d %s", w.Code, w.Body.String())
	}
	var req models.Requisition
	if err := db.Preload("Lines").First(&req).Error; err != nil {
		t.Fatal(err)
	}
	if req.EstimatedTotalCents != 2468 || len(req.Lines) != 1 || req.Lines[0].EstimatedPriceCents != 1234 || req.Lines[0].SupplierPartAuxiliaryID != "opaque-token" {
		t.Fatalf("unexpected requisition: %+v", req)
	}
	if w := call(); w.Code != http.StatusConflict {
		t.Fatalf("replay accepted: %d", w.Code)
	}
	var count int64
	if err := db.Model(&models.Requisition{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("requisitions=%d err=%v", count, err)
	}
}
