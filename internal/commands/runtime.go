package commands

import (
	"os"
	"path/filepath"

	"github.com/ontopix/engram/internal/cli"
)

// RegisterReference installs the complete reference command surface and its
// bounded recovery composition. All trust-aware commands use the same
// controller-owned registry path.
func RegisterReference(app *cli.App) {
	root, err := os.UserConfigDir()
	if err != nil {
		registerReferenceBase(app)
		registerHookConfigurationFailure(app, err)
		registerInitializationFailure(app, err)
		registerAcceptanceFailure(app, err)
		registerPullFailure(app, err)
		RegisterDoctor(app)
		return
	}
	RegisterReferenceAt(app, filepath.Join(root, "engram", "hook-trust-v1.json"))
}

// RegisterReferenceAt is the deterministic embedding and test variant.
func RegisterReferenceAt(app *cli.App, registryPath string) {
	registerReferenceBase(app)
	RegisterHooksAt(app, registryPath)
	RegisterInitializationAt(app, registryPath)
	RegisterAcceptanceAt(app, registryPath)
	RegisterPullAt(app, registryPath)
	recovery, err := newReferenceRecovery(registryPath)
	if err != nil {
		RegisterDoctor(app)
		return
	}
	RegisterDoctorWithRecovery(app, recovery)
}

func registerReferenceBase(app *cli.App) {
	RegisterPortable(app)
	RegisterManagedReads(app)
	RegisterAttachments(app)
	RegisterSetup(app)
	RegisterConfig(app)
	RegisterStaging(app)
	RegisterDraft(app)
	RegisterAcquisition(app)
	RegisterSync(app)
}
