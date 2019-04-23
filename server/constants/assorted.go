package constants

const (
	HooksRepoName = "admin/hooks"
	// The Hook API version used by the binary
	HookAPIVersion = 1
	// The minimum API version required by the hookRunner
	// NOTE: This is usually the same as the HookAPIVersion, but is still explicit in case minor changes are made to the API.
	// Usually, the hookrunner will demand an up-to-date daemon, and this is just used to error out predictably
	HookAPIVersionMin = HookAPIVersion
)
