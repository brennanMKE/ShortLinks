package reports

import "time"

// Campaign statuses reported by CampaignStatus.
const (
	CampaignLive      = "live"
	CampaignScheduled = "scheduled"
	CampaignEnded     = "ended"
	CampaignArchived  = "archived"
)

// CampaignDate returns the calendar date (YYYY-MM-DD) a campaign's starts_at
// or ends_at stands for. Those columns are date-only values stored as UTC
// midnight, so the date is read from the UTC fields, never converted to a
// time zone.
func CampaignDate(t time.Time) string { return t.UTC().Format(dayLayout) }

// CampaignStatus derives a campaign's status from its dates:
//
//   - archived wins over everything, including the dates.
//   - scheduled: starts_at is set and its date is after today.
//   - ended: ends_at is set and its date is before today.
//   - live: everything else. That includes a campaign with no dates at all, a
//     campaign with only a start date that has passed, and one with only an end
//     date that has not. Both bounds are inclusive, so a campaign is live on
//     its first and its last day.
//
// "Today" is now's calendar date in loc (nil means UTC), while the campaign
// dates are plain calendar dates, so the boundaries flip at local midnight.
// It is pure: it never reads the clock.
func CampaignStatus(startsAt, endsAt *time.Time, archived bool, now time.Time, loc *time.Location) string {
	if archived {
		return CampaignArchived
	}
	if loc == nil {
		loc = time.UTC
	}
	today := now.In(loc).Format(dayLayout)
	if startsAt != nil && today < CampaignDate(*startsAt) {
		return CampaignScheduled
	}
	if endsAt != nil && today > CampaignDate(*endsAt) {
		return CampaignEnded
	}
	return CampaignLive
}
