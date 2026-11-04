package leadfinder

import (
	"context"
	"strconv"

	"github.com/google/uuid"

	"github.com/warmbly/warmbly/internal/app/contact"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/pkg/emailverify"
)

// AddToContactsRequest turns selected leads into contacts.
type AddToContactsRequest struct {
	models.LeadSelection
	CampaignIDs []string `json:"campaign_ids"`
	CategoryIDs []string `json:"category_ids"`
	SegmentIDs  []string `json:"segment_ids"`
	// VerifiedOnly keeps out every lead whose address did not check as valid.
	VerifiedOnly bool `json:"verified_only"`
}

// AddToContactsResult says what became of the selection.
type AddToContactsResult struct {
	Selected   int                            `json:"selected"`
	Imported   int                            `json:"imported"`
	Updated    int                            `json:"updated"`
	Skipped    int                            `json:"skipped"`
	Failed     int                            `json:"failed"`
	NoEmail    int                            `json:"no_email"`
	Unverified int                            `json:"unverified"`
	Suppressed int                            `json:"suppressed"`
	Errors     []models.ContactImportRowError `json:"errors,omitempty"`
}

// leadColumns are the contact fields a lead fills, in cell order.
var leadColumns = []struct {
	target models.ContactImportColumnTarget
	custom string
	value  func(l *models.Lead) string
}{
	{models.ContactImportTargetEmail, "", func(l *models.Lead) string { return l.Email }},
	{models.ContactImportTargetCompany, "", func(l *models.Lead) string { return l.Name }},
	{models.ContactImportTargetPhone, "", func(l *models.Lead) string { return l.Phone }},
	{models.ContactImportTargetVerificationStatus, "", func(l *models.Lead) string {
		if e := primaryEmail(l); e != nil {
			switch e.Status {
			case string(emailverify.StatusValid), string(emailverify.StatusRisky), string(emailverify.StatusInvalid):
				return e.Status
			}
		}
		return ""
	}},
	{models.ContactImportTargetCustom, "website", func(l *models.Lead) string { return l.Website }},
	{models.ContactImportTargetCustom, "business_category", func(l *models.Lead) string { return l.Category }},
	{models.ContactImportTargetCustom, "address", func(l *models.Lead) string { return l.Address }},
	{models.ContactImportTargetCustom, "city", func(l *models.Lead) string { return l.City }},
	{models.ContactImportTargetCustom, "state", func(l *models.Lead) string { return l.Region }},
	{models.ContactImportTargetCustom, "postcode", func(l *models.Lead) string { return l.Postcode }},
	{models.ContactImportTargetCustom, "country", func(l *models.Lead) string { return l.Country }},
	{models.ContactImportTargetCustom, "rating", func(l *models.Lead) string { return fmtFloat(l.Rating) }},
	{models.ContactImportTargetCustom, "review_count", func(l *models.Lead) string { return fmtInt(l.ReviewCount) }},
	{models.ContactImportTargetCustom, "map_url", func(l *models.Lead) string { return l.MapsURL }},
	{models.ContactImportTargetCustom, "facebook_url", social("facebook")},
	{models.ContactImportTargetCustom, "instagram_url", social("instagram")},
	{models.ContactImportTargetCustom, "linkedin_url", social("linkedin")},
	{models.ContactImportTargetCustom, "website_platform", func(l *models.Lead) string { return l.Site.Platform }},
	{models.ContactImportTargetCustom, "lead_score", func(l *models.Lead) string { return strconv.Itoa(l.Score) }},
}

// AddToContacts imports the selected leads that have a usable address as
// contacts, optionally straight into campaigns, categories and segments.
func (s *Service) AddToContacts(ctx context.Context, orgID, userID uuid.UUID, req *AddToContactsRequest) (*AddToContactsResult, error) {
	ids, err := s.Resolve(ctx, orgID, &req.LeadSelection)
	if err != nil {
		return nil, err
	}
	res := &AddToContactsResult{Selected: len(ids)}
	var leads []models.Lead
	for start := 0; start < len(ids); start += 500 {
		batch, err := s.Repo.LeadsByIDs(ctx, orgID, ids[start:min(start+500, len(ids))])
		if err != nil {
			return nil, err
		}
		leads = append(leads, batch...)
	}
	emails := make([]string, 0, len(leads))
	for _, l := range leads {
		if l.Email != "" {
			emails = append(emails, l.Email)
		}
	}
	suppressed, err := s.Repo.SuppressedEmails(ctx, orgID, emails)
	if err != nil {
		return nil, err
	}
	var fresh []*models.Lead
	seen := map[string]bool{}
	for i := range leads {
		l := &leads[i]
		e := primaryEmail(l)
		switch {
		case l.Email == "":
			res.NoEmail++
		case suppressed[l.Email]:
			res.Suppressed++
		case req.VerifiedOnly && (e == nil || e.Status != string(emailverify.StatusValid)):
			res.Unverified++
		case seen[l.Email]:
			res.Skipped++
		default:
			seen[l.Email] = true
			fresh = append(fresh, l)
		}
	}
	if len(fresh) == 0 {
		return res, nil
	}

	mapping := make([]models.ContactImportColumnMapping, len(leadColumns))
	for i, c := range leadColumns {
		mapping[i] = models.ContactImportColumnMapping{Index: i, Target: c.target, CustomKey: c.custom}
	}
	rows := make([]contact.ImportRow, len(fresh))
	for i, l := range fresh {
		cells := make([]string, len(leadColumns))
		for j, c := range leadColumns {
			cells[j] = truncate(c.value(l), 500)
		}
		rows[i] = contact.ImportRow{Line: i + 1, Cells: cells}
	}
	subscribed := true
	opts := &models.ContactImportCommit{
		Mapping:           mapping,
		Dedup:             models.ContactImportDedupUpdate,
		CategoryIDs:       req.CategoryIDs,
		CampaignIDs:       req.CampaignIDs,
		SegmentIDs:        req.SegmentIDs,
		SubscribedDefault: &subscribed,
		Source:            models.ContactSourceLeadFinder,
		SourceDetail:      "Lead finder",
	}
	sink := &outcomeSink{outcomes: map[int]models.ContactImportRowOutcome{}}
	result, xerr := s.Contacts.RunImport(ctx, userID.String(), orgID, rows, opts, sink, nil)
	if xerr != nil {
		return nil, xerr
	}
	res.Imported, res.Updated, res.Failed = result.Imported, result.Updated, result.Failed
	res.Skipped += result.Skipped
	res.Errors = result.Errors

	links := map[uuid.UUID]uuid.UUID{}
	for i, l := range fresh {
		o, ok := sink.outcomes[i+1]
		if ok && o.ContactID != nil && o.Status != models.ContactImportRowFailed {
			links[l.ID] = *o.ContactID
		}
	}
	if err := s.Repo.LinkContacts(ctx, orgID, links); err != nil {
		return res, errx.New(errx.Internal, "the contacts were added but could not be linked back to their leads")
	}
	return res, nil
}

// outcomeSink keeps every row outcome of an import by line.
type outcomeSink struct {
	outcomes map[int]models.ContactImportRowOutcome
}

func (c *outcomeSink) Settle(_ context.Context, outcomes []models.ContactImportRowOutcome) error {
	for _, o := range outcomes {
		c.outcomes[o.Line] = o
	}
	return nil
}

func (c *outcomeSink) Cancelled(context.Context) bool { return false }
