package repository

import (
	"os"
	"path/filepath"
)

// ChangeWorkingDirectory changes the current process working directory and updates any CD file.
func ChangeWorkingDirectory(targetDir string) error {
	if targetDir == "" {
		return nil
	}
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	if cdFile := os.Getenv("INVARIANT_CD_FILE"); cdFile != "" {
		_ = os.WriteFile(cdFile, []byte(absDir), 0644)
	}
	if cdFile := os.Getenv("IR_CD_FILE"); cdFile != "" {
		_ = os.WriteFile(cdFile, []byte(absDir), 0644)
	}
	return os.Chdir(absDir)
}
