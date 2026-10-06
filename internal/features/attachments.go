package features

import (
	"context"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// AttachmentExtractor derives attachment features.
type AttachmentExtractor struct{}

func (AttachmentExtractor) Extract(_ context.Context, e email.Email) ([]Feature, error) {
	var pdf, img, sheet, cal bool
	types := map[string]struct{}{}
	for _, a := range e.Attach {
		ct := strings.ToLower(a.ContentType)
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = ct[:i]
		}
		ext := strings.ToLower(filepath.Ext(a.Filename))
		if ct == "" || ct == "application/octet-stream" {
			ct = strings.ToLower(mime.TypeByExtension(ext))
		}
		switch {
		case ct == "application/pdf" || ext == ".pdf":
			pdf = true
		case strings.HasPrefix(ct, "image/"):
			img = true
		case strings.Contains(ct, "spreadsheet") || strings.Contains(ct, "excel") || ct == "text/csv" || ext == ".csv" || ext == ".xls" || ext == ".xlsx":
			sheet = true
		case ct == "text/calendar" || ext == ".ics":
			cal = true
		}
		if ct != "" {
			types[ct] = struct{}{}
		}
	}
	out := []Feature{
		{AttachmentCount, strconv.Itoa(len(e.Attach)), 1, SourceParser},
		{AttachmentHasPDF, boolStr(pdf), 1, SourceParser},
		{AttachmentHasImage, boolStr(img), 1, SourceParser},
		{AttachmentHasSpreadsheet, boolStr(sheet), 1, SourceParser},
		{AttachmentHasCalendar, boolStr(cal), 1, SourceParser},
	}
	for t := range types {
		out = append(out, Feature{AttachmentTypes, t, 1, SourceParser})
	}
	return out, nil
}
