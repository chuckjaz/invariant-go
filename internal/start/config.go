package start

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the configuration in the YAML file.
type Config struct {
	Common   map[string]map[string]string `yaml:"common,omitempty"`
	Services []ServiceConfig              `yaml:"services"`
}

// StringArray allows a YAML field to be parsed as either a single string or a slice of strings.
type StringArray []string

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (a *StringArray) UnmarshalYAML(value *yaml.Node) error {
	var multi []string
	err := value.Decode(&multi)
	if err != nil {
		var single string
		err := value.Decode(&single)
		if err != nil {
			return err
		}
		*a = []string{single}
	} else {
		*a = multi
	}
	return nil
}

// ServiceConfig represents a single service to start.
type ServiceConfig struct {
	Command string            `yaml:"command"`
	Use     StringArray       `yaml:"use,omitempty"`
	Args    map[string]string `yaml:"args"`
}

// LoadConfig reads and parses a YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file '%s': %w", path, err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to process config file '%s': %w", path, err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for config file '%s': %w", path, err)
	}
	baseDir := filepath.Dir(absPath)

	// Apply substitutions
	for i := range config.Services {
		svc := &config.Services[i]

		if len(svc.Use) > 0 {
			if svc.Args == nil {
				svc.Args = make(map[string]string)
			}
			for _, useName := range svc.Use {
				commonArgs, ok := config.Common[useName]
				if !ok {
					return nil, fmt.Errorf("service '%s' uses undefined common set '%s'", svc.Command, useName)
				}
				for k, v := range commonArgs {
					if _, exists := svc.Args[k]; !exists {
						svc.Args[k] = v
					}
				}
			}
		}

		for k, v := range svc.Args {
			svc.Args[k] = SubstituteString(v, baseDir)
		}
	}

	return &config, nil
}

// varRegex matches environment variables ($VAR_NAME), tilde (~), asterisk (*), and escaped characters (\$, \~, \*, \\).
// It uses named capture groups for clarity:
// - `escaped`: Matches '\$', '\~', '\*' or '\\'
// - `tilde`: Matches '~'
// - `star`: Matches '*'
// - `varName`: Matches the name of an environment variable after '$'
var substitutionRegex = regexp.MustCompile(`\\(?P<escaped>[~$*])|\\(?P<escaped_backslash>\\)|(?P<tilde>~)|(?P<star>\*)|(?P<varName>\$[a-zA-Z0-9_]+)`)

// SubstituteString processes a string for environment variable substitutions
// of the form $NAME, replaced by the environment variable NAME. It also
// substitutes '~' with the user's home directory and '*' with the baseDir.
// A backslash '\' in front of '$', '~', '*', or '\' escapes the character and the
// backslash is removed.
func SubstituteString(in string, baseDir string) string {
	homeDir, _ := os.UserHomeDir()

	return substitutionRegex.ReplaceAllStringFunc(in, func(match string) string {
		// Check for escaped characters first
		if strings.HasPrefix(match, `\`) {
			// If it's an escaped backslash, return a single backslash
			if match == `\\` {
				return `\`
			}
			// Otherwise, return the character after the backslash (e.g., '$' for '\$')
			return string(match[1])
		}

		// Check for tilde substitution
		if match == "~" {
			if homeDir != "" {
				return homeDir
			}
			return "~" // Fallback if home directory is not found
		}

		// Check for star substitution
		if match == "*" {
			return baseDir
		}

		// Check for environment variable substitution
		if strings.HasPrefix(match, "$") {
			varName := match[1:] // Remove the '$' prefix
			if val, exists := os.LookupEnv(varName); exists {
				return val
			}
			return "" // If env var not found, replace with empty string
		}

		return match // Should not happen if regex is comprehensive
	})
}
