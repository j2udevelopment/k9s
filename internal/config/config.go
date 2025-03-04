// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v2"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// Config tracks K9s configuration options.
type Config struct {
	K9s      *K9s `yaml:"k9s"`
	conn     client.Connection
	settings data.KubeSettings
}

// K9sHome returns k9s configs home directory.
func K9sHome() string {
	if isEnvSet(K9sEnvConfigDir) {
		return os.Getenv(K9sEnvConfigDir)
	}

	xdgK9sHome, err := xdg.ConfigFile(AppName)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to create configuration directory for k9s")
	}

	return xdgK9sHome
}

// NewConfig creates a new default config.
func NewConfig(ks data.KubeSettings) *Config {
	return &Config{
		settings: ks,
		K9s:      NewK9s(nil, ks),
	}
}

// ContextAliasesPath returns a context specific aliases file spec.
func (c *Config) ContextAliasesPath() string {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return ""
	}

	return AppContextAliasesFile(ct.ClusterName, c.K9s.activeContextName)
}

// ContextPluginsPath returns a context specific plugins file spec.
func (c *Config) ContextPluginsPath() string {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return ""
	}

	return AppContextPluginsFile(ct.ClusterName, c.K9s.activeContextName)
}

// Refine the configuration based on cli args.
func (c *Config) Refine(flags *genericclioptions.ConfigFlags, k9sFlags *Flags, cfg *client.Config) error {
	if isSet(flags.Context) {
		if _, err := c.K9s.ActivateContext(*flags.Context); err != nil {
			return err
		}
	} else {
		n, err := cfg.CurrentContextName()
		if err != nil {
			return err
		}
		_, err = c.K9s.ActivateContext(n)
		if err != nil {
			return err
		}
	}
	log.Debug().Msgf("Active Context %q", c.K9s.ActiveContextName())

	var ns string
	switch {
	case k9sFlags != nil && IsBoolSet(k9sFlags.AllNamespaces):
		ns = client.NamespaceAll
	case isSet(flags.Namespace):
		ns = *flags.Namespace
	default:
		nss, err := c.K9s.ActiveContextNamespace()
		if err != nil {
			return err
		}
		ns = nss
	}
	if ns == "" {
		ns = client.DefaultNamespace
	}
	if err := c.SetActiveNamespace(ns); err != nil {
		return err
	}

	return data.EnsureDirPath(c.K9s.GetScreenDumpDir(), data.DefaultDirMod)
}

// Reset resets the context to the new current context/cluster.
func (c *Config) Reset() {
	c.K9s.Reset()
}

func (c *Config) SetCurrentContext(n string) (*data.Context, error) {
	ct, err := c.K9s.ActivateContext(n)
	if err != nil {
		return nil, fmt.Errorf("set current context %q failed: %w", n, err)
	}

	return ct, nil
}

// CurrentContext fetch the configuration active context.
func (c *Config) CurrentContext() (*data.Context, error) {
	return c.K9s.ActiveContext()
}

// ActiveNamespace returns the active namespace in the current context.
// If none found return the empty ns.
func (c *Config) ActiveNamespace() string {
	ns, err := c.K9s.ActiveContextNamespace()
	if err != nil {
		log.Error().Err(err).Msgf("Unable to assert active namespace. Using default")
		ns = client.DefaultNamespace
	}

	return ns
}

// ValidateFavorites ensure favorite ns are legit.
func (c *Config) ValidateFavorites() {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return
	}
	ct.Validate(c.conn, c.settings)
}

// FavNamespaces returns fav namespaces in the current context.
func (c *Config) FavNamespaces() []string {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return nil
	}

	return ct.Namespace.Favorites
}

// SetActiveNamespace set the active namespace in the current context.
func (c *Config) SetActiveNamespace(ns string) error {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return err
	}

	return ct.Namespace.SetActive(ns, c.settings)
}

// ActiveView returns the active view in the current context.
func (c *Config) ActiveView() string {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		return data.DefaultView
	}
	cmd := ct.View.Active
	if c.K9s.manualCommand != nil && *c.K9s.manualCommand != "" {
		cmd = *c.K9s.manualCommand
		// We reset the manualCommand property because
		// the command-line switch should only be considered once,
		// on startup.
		*c.K9s.manualCommand = ""
	}

	return cmd
}

// SetActiveView sets current context active view.
func (c *Config) SetActiveView(view string) {
	if ct, err := c.K9s.ActiveContext(); err == nil {
		ct.View.Active = view
	}
}

// GetConnection return an api server connection.
func (c *Config) GetConnection() client.Connection {
	return c.conn
}

// SetConnection set an api server connection.
func (c *Config) SetConnection(conn client.Connection) {
	c.conn = conn
	if conn != nil {
		c.K9s.resetConnection(conn)
	}
}

func (c *Config) ActiveContextName() string {
	return c.K9s.activeContextName
}

// Load loads K9s configuration from file.
func (c *Config) Load(path string) error {
	f, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var cfg Config
	if err := yaml.Unmarshal(f, &cfg); err != nil {
		return err
	}
	if cfg.K9s != nil {
		c.K9s.Refine(cfg.K9s)
	}
	if c.K9s.Logger == nil {
		c.K9s.Logger = NewLogger()
	}
	return nil
}

// Save configuration to disk.
func (c *Config) Save() error {
	c.Validate()
	if err := c.K9s.Save(); err != nil {
		return err
	}
	return c.SaveFile(AppConfigFile)
}

// SaveFile K9s configuration to disk.
func (c *Config) SaveFile(path string) error {
	if err := data.EnsureDirPath(path, data.DefaultDirMod); err != nil {
		return err
	}
	cfg, err := yaml.Marshal(c)
	if err != nil {
		log.Error().Msgf("[Config] Unable to save K9s config file: %v", err)
		return err
	}
	return os.WriteFile(path, cfg, 0644)
}

// Validate the configuration.
func (c *Config) Validate() {
	c.K9s.Validate(c.conn, c.settings)
}

// Dump debug...
func (c *Config) Dump(msg string) {
	ct, err := c.K9s.ActiveContext()
	if err != nil {
		log.Debug().Msgf("Current Contexts: %s\n", ct.ClusterName)
	}
}

// YamlExtension tries to find the correct extension for a YAML file
func YamlExtension(path string) string {
	if !isYamlFile(path) {
		log.Error().Msgf("Config: File %s is not a yaml file", path)
		return path
	}

	// Strip any extension, if there is no extension the path will remain unchanged
	path = strings.TrimSuffix(path, filepath.Ext(path))
	result := path + ".yml"

	if _, err := os.Stat(result); os.IsNotExist(err) {
		return path + ".yaml"
	}

	return result
}

// ----------------------------------------------------------------------------
// Helpers...

func isSet(s *string) bool {
	return s != nil && len(*s) > 0
}

func isYamlFile(file string) bool {
	ext := filepath.Ext(file)
	return ext == ".yml" || ext == ".yaml"
}
