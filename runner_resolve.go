package logos

import "fmt"

// resolveRunner selects the appropriate commandRunner based on Config.
// A non-nil cfg.testRunner takes priority (used by tests).
// Otherwise, Sandbox=false returns localRunner; Sandbox=true returns temenos client.
func resolveRunner(cfg *Config) (commandRunner, error) {
	if cfg.testRunner != nil {
		return cfg.testRunner, nil
	}
	if !cfg.Sandbox {
		return &localRunner{}, nil
	}
	tc, err := newClient(cfg.SandboxAddr)
	if err != nil {
		return nil, fmt.Errorf("logos: sandbox required but temenos unreachable: %w", err)
	}
	return tc, nil
}
