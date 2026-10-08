//go:build !linux && !darwin

package pipeline

import (
	"errors"
)

var errSecureBriefMigrationUnsupported = errors.New("secure controller brief migration is unsupported on this platform")

func secureMigrateLegacyBrief(string, string, map[string][]byte) error {
	return errSecureBriefMigrationUnsupported
}

func secureRemoveLegacyBrief(string, string, map[string][]byte) error {
	return errSecureBriefMigrationUnsupported
}

func secureRemoveEmptyLegacyRunRoot(string, string) error {
	return errSecureBriefMigrationUnsupported
}

func secureRemoveEmptyCanonicalBrief(string, string) error {
	return errSecureBriefMigrationUnsupported
}

func openBriefDirectory(string, ...string) (int, error) {
	return -1, errSecureBriefMigrationUnsupported
}

func closeControllerBriefRoot(int) error { return errSecureBriefMigrationUnsupported }

func createInitialBriefAt(int, string, string) (BriefDocument, error) {
	return BriefDocument{}, errSecureBriefMigrationUnsupported
}

func appendBriefClarificationAt(int, string, string, string, string) (BriefDocument, error) {
	return BriefDocument{}, errSecureBriefMigrationUnsupported
}

func listBriefStoreVersionsAt(int, string) ([]BriefVersion, error) {
	return nil, errSecureBriefMigrationUnsupported
}

func readBriefStoreDocumentAt(int, string, string) (BriefDocument, error) {
	return BriefDocument{}, errSecureBriefMigrationUnsupported
}
