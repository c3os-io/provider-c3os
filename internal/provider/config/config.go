package config

type P2P struct {
	NetworkToken string `yaml:"network_token,omitempty"`
	NetworkID    string `yaml:"network_id,omitempty"`
	Role         string `yaml:"role,omitempty"`
	DNS          bool   `yaml:"dns,omitempty"`
	LogLevel     string `yaml:"loglevel,omitempty"`
	VPN          VPN    `yaml:"vpn,omitempty"`

	MinimumNodes int  `yaml:"minimum_nodes,omitempty"`
	DisableDHT   bool `yaml:"disable_dht,omitempty"`
	Auto         Auto `yaml:"auto,omitempty"`

	DynamicRoles bool   `yaml:"dynamic_roles,omitempty"`
	Distribution string `yaml:"distribution,omitempty"`
}

type VPN struct {
	Create *bool             `yaml:"create,omitempty"`
	Use    *bool             `yaml:"use,omitempty"`
	Env    map[string]string `yaml:"env,omitempty"`
}

// If no setting is provided by the user,
// we assume that we are going to create and use the VPN
// for the network layer of our cluster.
func (p P2P) UseVPNWithKubernetes() bool {
	return p.VPNNeedsCreation() && (p.VPN.Use == nil || *p.VPN.Use)
}

func (p P2P) VPNNeedsCreation() bool {
	return p.VPN.Create == nil || *p.VPN.Create
}

type Config struct {
	P2P       *P2P    `yaml:"p2p,omitempty"`
	K3sAgent  K3s     `yaml:"k3s-agent,omitempty"`
	K3s       K3s     `yaml:"k3s,omitempty"`
	KubeVIP   KubeVIP `yaml:"kubevip,omitempty"`
	K0sWorker K0s     `yaml:"k0s-worker,omitempty"`
	K0s       K0s     `yaml:"k0s,omitempty"`
}

func (c Config) IsK3sAgentEnabled() bool {
	return c.K3sAgent.IsEnabled()
}

func (c Config) IsK3sEnabled() bool {
	return c.K3s.IsEnabled()
}

func (c Config) IsK3sDistributionEnabled() bool {
	return c.IsK3sAgentEnabled() || c.IsK3sEnabled()
}

func (c Config) IsK0sEnabled() bool {
	return c.K0s.IsEnabled()
}

func (c Config) IsK0sWorkerEnabled() bool {
	return c.K0sWorker.IsEnabled()
}

func (c Config) IsK0sDistributionEnabled() bool {
	return c.IsK0sEnabled() || c.IsK0sWorkerEnabled()
}

func (c Config) IsAKubernetesDistributionEnabled() bool {
	return c.IsK3sAgentEnabled() || c.IsK3sEnabled() || c.IsK0sEnabled() || c.IsK0sWorkerEnabled()
}

func (c Config) K8sServiceName() string {
	if c.IsK3sAgentEnabled() {
		return "k3s-agent"
	}

	if c.IsK3sEnabled() {
		return "k3s"
	}

	if c.IsK0sEnabled() {
		return "k0scontroller"
	}

	if c.IsK0sWorkerEnabled() {
		return "k0sworker"
	}

	return ""
}

func (c Config) K8sNodeRole() string {
	if c.IsK3sAgentEnabled() {
		return "agent"
	}

	if c.IsK3sEnabled() {
		return "server"
	}

	if c.IsK0sEnabled() {
		return "controller"
	}

	if c.IsK0sWorkerEnabled() {
		return "worker"
	}

	return ""
}

func (c Config) K8sEnv() map[string]string {
	if c.IsK3sEnabled() {
		return c.K3s.Env
	}

	if c.IsK3sAgentEnabled() {
		return c.K3sAgent.Env
	}

	if c.IsK0sEnabled() {
		return c.K0s.Env
	}

	if c.IsK0sWorkerEnabled() {
		return c.K0sWorker.Env
	}

	return nil
}

func (c Config) K8sArgs() []string {
	if c.IsK3sEnabled() {
		return c.K3s.Args
	}

	if c.IsK3sAgentEnabled() {
		return c.K3sAgent.Args
	}

	if c.IsK0sEnabled() {
		return c.K0s.Args
	}

	if c.IsK0sWorkerEnabled() {
		return c.K0sWorker.Args
	}

	return nil
}

type KubeVIP struct {
	Args        []string `yaml:"args,omitempty"`
	EIP         string   `yaml:"eip,omitempty"`
	ManifestURL string   `yaml:"manifest_url,omitempty"`
	Interface   string   `yaml:"interface,omitempty"`
	Enable      *bool    `yaml:"enable,omitempty"`
	StaticPod   bool     `yaml:"static_pod,omitempty"`
}

func (k KubeVIP) IsEnabled() bool {
	return (k.Enable == nil && k.EIP != "") || (k.Enable != nil && *k.Enable)
}

type Auto struct {
	Enable *bool `yaml:"enable,omitempty"`
	HA     HA    `yaml:"ha,omitempty"`
}

func (a Auto) IsEnabled() bool {
	return a.Enable == nil || (a.Enable != nil && *a.Enable)
}

func (ha HA) IsEnabled() bool {
	return (ha.Enable != nil && *ha.Enable) || (ha.Enable == nil && ha.MasterNodes != nil)
}

type HA struct {
	Enable      *bool  `yaml:"enable,omitempty"`
	ExternalDB  string `yaml:"external_db,omitempty"`
	MasterNodes *int   `yaml:"master_nodes,omitempty"`
}

type K8sConfig struct {
	Env              map[string]string `yaml:"env,omitempty"`
	ReplaceEnv       bool              `yaml:"replace_env,omitempty"`
	ReplaceArgs      bool              `yaml:"replace_args,omitempty"`
	Args             []string          `yaml:"args,omitempty"`
	Enabled          bool              `yaml:"enabled,omitempty"`
	EmbeddedRegistry bool              `yaml:"embedded_registry,omitempty"`
}

type K3s struct {
	Env              map[string]string `yaml:"env,omitempty"`
	ReplaceEnv       bool              `yaml:"replace_env,omitempty"`
	ReplaceArgs      bool              `yaml:"replace_args,omitempty"`
	Args             []string          `yaml:"args,omitempty"`
	Enabled          bool              `yaml:"enabled,omitempty"`
	EmbeddedRegistry bool              `yaml:"embedded_registry,omitempty"`
}

func (k K3s) IsEnabled() bool {
	return k.Enabled
}

func (k K3s) AppendArgs(other []string) []string {
	if k.ReplaceArgs {
		return k.Args
	}

	return append(other, k.Args...)
}

type K0s struct {
	Env              map[string]string `yaml:"env,omitempty"`
	ReplaceEnv       bool              `yaml:"replace_env,omitempty"`
	ReplaceArgs      bool              `yaml:"replace_args,omitempty"`
	Args             []string          `yaml:"args,omitempty"`
	Enabled          bool              `yaml:"enabled,omitempty"`
	EmbeddedRegistry bool              `yaml:"embedded_registry,omitempty"`
}

func (k K0s) IsEnabled() bool {
	return k.Enabled
}

func (k K0s) AppendArgs(other []string) []string {
	if k.ReplaceArgs {
		return k.Args
	}

	return append(other, k.Args...)
}
