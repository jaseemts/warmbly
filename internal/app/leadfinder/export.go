package leadfinder

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
)

// ExportColumn is one column a lead export can carry.
type ExportColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	value func(l *models.Lead) string
}

func fmtFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func fmtInt(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func primaryEmail(l *models.Lead) *models.LeadEmail {
	for i := range l.Emails {
		if l.Emails[i].Email == l.Email {
			return &l.Emails[i]
		}
	}
	return nil
}

func social(kind string) func(l *models.Lead) string {
	return func(l *models.Lead) string { return l.Socials[kind] }
}

// ExportColumns are every column, in file order.
var ExportColumns = []ExportColumn{
	{"name", "Name", func(l *models.Lead) string { return l.Name }},
	{"category", "Category", func(l *models.Lead) string { return l.Category }},
	{"email", "Email", func(l *models.Lead) string { return l.Email }},
	{"email_status", "Email status", func(l *models.Lead) string {
		if e := primaryEmail(l); e != nil {
			return e.Status
		}
		return ""
	}},
	{"other_emails", "Other emails", func(l *models.Lead) string {
		var out []string
		for _, e := range l.Emails {
			if e.Email != l.Email && e.Status != "suppressed" && e.Status != "invalid" {
				out = append(out, e.Email)
			}
		}
		return strings.Join(out, "; ")
	}},
	{"phone", "Phone", func(l *models.Lead) string { return l.Phone }},
	{"website", "Website", func(l *models.Lead) string { return l.Website }},
	{"domain", "Domain", func(l *models.Lead) string { return l.Domain }},
	{"address", "Address", func(l *models.Lead) string { return l.Address }},
	{"street", "Street", func(l *models.Lead) string { return l.Street }},
	{"city", "City", func(l *models.Lead) string { return l.City }},
	{"region", "Region", func(l *models.Lead) string { return l.Region }},
	{"postcode", "Postcode", func(l *models.Lead) string { return l.Postcode }},
	{"country", "Country", func(l *models.Lead) string { return l.Country }},
	{"latitude", "Latitude", func(l *models.Lead) string { return strconv.FormatFloat(l.Latitude, 'f', 6, 64) }},
	{"longitude", "Longitude", func(l *models.Lead) string { return strconv.FormatFloat(l.Longitude, 'f', 6, 64) }},
	{"rating", "Rating", func(l *models.Lead) string { return fmtFloat(l.Rating) }},
	{"review_count", "Reviews", func(l *models.Lead) string { return fmtInt(l.ReviewCount) }},
	{"price_level", "Price level", func(l *models.Lead) string { return fmtInt(l.PriceLevel) }},
	{"business_status", "Business status", func(l *models.Lead) string { return l.BusinessStatus }},
	{"hours", "Hours", func(l *models.Lead) string { return strings.Join(l.Hours, "; ") }},
	{"facebook", "Facebook", social("facebook")},
	{"instagram", "Instagram", social("instagram")},
	{"linkedin", "LinkedIn", social("linkedin")},
	{"twitter", "X (Twitter)", social("twitter")},
	{"tiktok", "TikTok", social("tiktok")},
	{"youtube", "YouTube", social("youtube")},
	{"site_platform", "Website platform", func(l *models.Lead) string { return l.Site.Platform }},
	{"site_signals", "Website signals", func(l *models.Lead) string { return strings.Join(l.Site.Signals, "; ") }},
	{"chain", "Chain", func(l *models.Lead) string { return l.Chain }},
	{"score", "Score", func(l *models.Lead) string { return strconv.Itoa(l.Score) }},
	{"status", "Status", func(l *models.Lead) string { return string(l.Status) }},
	{"tags", "Tags", func(l *models.Lead) string { return strings.Join(l.Tags, "; ") }},
	{"notes", "Notes", func(l *models.Lead) string { return l.Notes }},
	{"maps_url", "Map link", func(l *models.Lead) string { return l.MapsURL }},
	{"provider", "Source", func(l *models.Lead) string { return providerMeta[l.Provider].Name }},
	{"external_id", "Source id", func(l *models.Lead) string { return l.ExternalID }},
	{"first_seen_at", "First seen", func(l *models.Lead) string { return l.FirstSeenAt.UTC().Format(time.RFC3339) }},
}

// DefaultExportColumns is what an export carries when none are picked.
var DefaultExportColumns = []string{
	"name", "category", "email", "email_status", "other_emails", "phone", "website", "address", "city", "region",
	"postcode", "country", "rating", "review_count", "facebook", "instagram", "linkedin", "site_platform",
	"site_signals", "score", "status", "tags", "maps_url",
}

// ExportRequest asks for a file of leads.
type ExportRequest struct {
	models.LeadSelection
	Format  string   `json:"format"`
	Columns []string `json:"columns"`
}

// Export writes the selected leads to w and returns the content type and filename.
func (s *Service) Export(ctx context.Context, orgID uuid.UUID, req *ExportRequest, w io.Writer) (string, string, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" && format != "json" {
		return "", "", errx.New(errx.BadRequest, "format must be csv, xlsx or json")
	}
	keys := req.Columns
	if len(keys) == 0 {
		keys = DefaultExportColumns
	}
	cols := make([]ExportColumn, 0, len(keys))
	for _, k := range keys {
		c := columnByKey(k)
		if c == nil {
			return "", "", errx.New(errx.BadRequest, "unknown export column: "+k)
		}
		cols = append(cols, *c)
	}
	ids, err := s.Resolve(ctx, orgID, &req.LeadSelection)
	if err != nil {
		return "", "", err
	}
	leads := make([]models.Lead, 0, len(ids))
	for start := 0; start < len(ids); start += 500 {
		batch, err := s.Repo.LeadsByIDs(ctx, orgID, ids[start:min(start+500, len(ids))])
		if err != nil {
			return "", "", err
		}
		leads = append(leads, batch...)
	}
	name := "leads-" + time.Now().UTC().Format("2006-01-02") + "." + format
	switch format {
	case "json":
		out := make([]map[string]string, len(leads))
		for i := range leads {
			row := make(map[string]string, len(cols))
			for _, c := range cols {
				row[c.Key] = c.value(&leads[i])
			}
			out[i] = row
		}
		return "application/json", name, json.NewEncoder(w).Encode(out)
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", name, writeLeadsXLSX(w, leads, cols)
	}
	cw := csv.NewWriter(w)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Label
	}
	if err := cw.Write(header); err != nil {
		return "", "", err
	}
	for i := range leads {
		row := make([]string, len(cols))
		for j, c := range cols {
			row[j] = cellSafe(c.value(&leads[i]))
		}
		if err := cw.Write(row); err != nil {
			return "", "", err
		}
	}
	cw.Flush()
	return "text/csv; charset=utf-8", name, cw.Error()
}

func columnByKey(k string) *ExportColumn {
	for i := range ExportColumns {
		if ExportColumns[i].Key == k {
			return &ExportColumns[i]
		}
	}
	return nil
}

// phoneShape is a value that is only a phone number, which no spreadsheet runs.
var phoneShape = regexp.MustCompile(`^\+[0-9 ().\-/]+$`)

// cellSafe keeps a provider's text from running as a spreadsheet formula.
func cellSafe(v string) string {
	if v == "" || !strings.ContainsRune("=+-@\t\r", rune(v[0])) || phoneShape.MatchString(v) {
		return v
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return v
	}
	return "'" + v
}

func writeLeadsXLSX(w io.Writer, leads []models.Lead, cols []ExportColumn) error {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Leads"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	bold, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#F1F5F9"}},
	})
	_ = sw.SetColWidth(1, len(cols), 22)
	if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	header := make([]any, len(cols))
	for i, c := range cols {
		header[i] = excelize.Cell{StyleID: bold, Value: c.Label}
	}
	if err := sw.SetRow("A1", header); err != nil {
		return err
	}
	numeric := map[string]bool{"latitude": true, "longitude": true, "rating": true, "review_count": true, "price_level": true, "score": true}
	for r := range leads {
		row := make([]any, len(cols))
		for i, c := range cols {
			v := c.value(&leads[r])
			if numeric[c.Key] && v != "" {
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					row[i] = n
					continue
				}
			}
			row[i] = cellSafe(v)
		}
		cell, _ := excelize.CoordinatesToCellName(1, r+2)
		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	return f.Write(w)
}
