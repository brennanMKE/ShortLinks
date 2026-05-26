package audit

// Action constants are the canonical event-type strings written to
// audit_log.action. They reproduce the PRD's Action catalogue verbatim; every
// call site references one of these rather than a string literal so a typo
// cannot silently fragment the log.
const (
	// Account lifecycle.
	ActionAccountRegistrationStarted = "account.registration_started" // actor NULL (pre-auth)
	ActionAccountRegistered          = "account.registered"
	ActionAccountLogin               = "account.login"
	ActionAccountLogout              = "account.logout"
	ActionAccountRecoveryStarted     = "account.recovery_started" // actor NULL (pre-auth)
	ActionAccountRecovered           = "account.recovered"
	// account.deactivated / account.reactivated are admin-on-other-user actions
	// that belong to admin user management (#0028). They have no call site yet;
	// the constants are defined here so #0028 can use the same API trivially.
	ActionAccountDeactivated = "account.deactivated"
	ActionAccountReactivated = "account.reactivated"

	// Credential lifecycle.
	ActionCredentialAdded   = "credential.added"
	ActionCredentialRevoked = "credential.revoked"

	// Link lifecycle.
	ActionLinkCreated     = "link.created"
	ActionLinkDeactivated = "link.deactivated"
	ActionLinkReactivated = "link.reactivated"
	ActionLinkDenied      = "link.denied"

	// URL filter rule lifecycle.
	ActionURLFilterCreated = "url_filter.created"
	ActionURLFilterUpdated = "url_filter.updated"
	ActionURLFilterDeleted = "url_filter.deleted"

	// Settings.
	ActionSettingsUpdated = "settings.updated"
)

// Target-type constants are the canonical values written to
// audit_log.target_type. They mirror the PRD's enumerated entity kinds.
const (
	TargetLink       = "link"
	TargetUser       = "user"
	TargetCredential = "credential"
	TargetSettings   = "settings"
	TargetURLFilter  = "url_filter"
)
