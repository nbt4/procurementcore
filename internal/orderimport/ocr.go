package orderimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxOCRPages = 20

// ExtractTextWithOCR uses the PDF text layer when available and local OCR for
// scanned documents. No document bytes are sent to Jev or another service.
func ExtractTextWithOCR(ctx context.Context, data []byte) (string, int, bool, error) {
	value, pages, err := ExtractText(data)
	if err == nil {
		return value, pages, false, nil
	}
	if pages == 0 || !strings.Contains(err.Error(), "keinen ausreichend maschinenlesbaren Text") {
		return "", pages, false, err
	}
	if pages > maxOCRPages {
		return "", pages, false, fmt.Errorf("gescannte PDFs dürfen höchstens %d Seiten enthalten", maxOCRPages)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "procurement-ocr-")
	if err != nil {
		return "", pages, false, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "source.pdf")
	if err := os.WriteFile(input, data, 0600); err != nil {
		return "", pages, false, err
	}
	var output strings.Builder
	for page := 1; page <= pages; page++ {
		image := filepath.Join(dir, fmt.Sprintf("page-%03d", page))
		command := exec.CommandContext(ctx, "pdftoppm", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), "-scale-to", "1800", "-png", "-singlefile", input, image)
		if detail, runErr := command.CombinedOutput(); runErr != nil {
			return "", pages, false, ocrError(ctx, "PDF-Seite konnte nicht gerendert werden", runErr, detail)
		}
		command = exec.CommandContext(ctx, "tesseract", image+".png", "stdout", "-l", "deu+eng")
		pageText, runErr := command.Output()
		if runErr != nil {
			var detail []byte
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				detail = exitErr.Stderr
			}
			return "", pages, false, ocrError(ctx, "Texterkennung fehlgeschlagen", runErr, detail)
		}
		os.Remove(image + ".png")
		if output.Len()+len(pageText) > maxExtractedText {
			return "", pages, false, errors.New("erkannter PDF-Text ist zu groß")
		}
		output.Write(pageText)
		output.WriteByte('\n')
	}
	value = strings.TrimSpace(output.String())
	if len([]rune(value)) < 20 {
		return "", pages, false, errors.New("aus dem gescannten PDF konnte kein lesbarer Text erkannt werden")
	}
	return value, pages, true, nil
}

func ocrError(ctx context.Context, message string, err error, detail []byte) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: Zeitlimit überschritten", message)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s: OCR-Werkzeuge fehlen auf dem Server", message)
	}
	_ = detail // Tool diagnostics must not expose document text to the API response.
	return fmt.Errorf("%s: %w", message, err)
}
