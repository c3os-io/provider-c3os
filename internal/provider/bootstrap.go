package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kairos-io/kairos-sdk/bus"
	"github.com/kairos-io/kairos-sdk/machine"
	"github.com/kairos-io/kairos-sdk/machine/openrc"
	"github.com/kairos-io/kairos-sdk/machine/systemd"
	"github.com/kairos-io/kairos-sdk/types"
	"github.com/kairos-io/kairos-sdk/utils"
	providerConfig "github.com/kairos-io/provider-kairos/v2/internal/provider/config"
	"github.com/kairos-io/provider-kairos/v2/internal/role"
	p2p "github.com/kairos-io/provider-kairos/v2/internal/role/p2p"
	edgeVPNClient "github.com/mudler/edgevpn/api/client"

	"github.com/kairos-io/provider-kairos/v2/internal/services"

	"github.com/kairos-io/kairos-agent/v2/pkg/config"
	"github.com/mudler/edgevpn/api/client/service"
	"github.com/mudler/go-pluggable"
)

func Bootstrap(e *pluggable.Event) pluggable.EventResponse {
	cfg := &bus.BootstrapPayload{}
	err := json.Unmarshal([]byte(e.Data), cfg)
	if err != nil {
		return ErrorEvent("Failed reading JSON input: %s input '%s'", err.Error(), e.Data)
	}

	c := &config.Config{}
	prvConfig := &providerConfig.Config{}
	err = config.FromString(cfg.Config, c)
	if err != nil {
		return ErrorEvent("Failed reading JSON input: %s input '%s'", err.Error(), cfg.Config)
	}

	err = config.FromString(cfg.Config, prvConfig)
	if err != nil {
		return ErrorEvent("Failed reading JSON input: %s input '%s'", err.Error(), cfg.Config)
	}
	// TODO: this belong to a systemd service that is started instead

	p2pBlockDefined := prvConfig.P2P != nil
	tokenNotDefined := (p2pBlockDefined && prvConfig.P2P.NetworkToken == "") || !p2pBlockDefined
	skipAuto := p2pBlockDefined && !prvConfig.P2P.Auto.IsEnabled()

	if prvConfig.P2P == nil && !prvConfig.IsAKubernetesDistributionEnabled() {
		return pluggable.EventResponse{State: fmt.Sprintf("no kubernetes distribution configuration. nothing to do: %s", cfg.Config)}
	}

	utils.SH("kairos-agent run-stage kairos-agent.bootstrap") //nolint:errcheck
	bus.RunHookScript("/usr/bin/kairos-agent.bootstrap.hook") //nolint:errcheck

	logLevel := "debug"

	if p2pBlockDefined && prvConfig.P2P.LogLevel != "" {
		logLevel = prvConfig.P2P.LogLevel
	}

	logger := types.NewKairosLogger("provider", logLevel, false)

	// Do onetimebootstrap if a Kubernetes distribution is enabled.
	// Those blocks are not required to be enabled in case of a kairos
	// full automated setup. Otherwise, they must be explicitly enabled.
	if (tokenNotDefined && prvConfig.IsAKubernetesDistributionEnabled()) || skipAuto {
		err := oneTimeBootstrap(logger, prvConfig, func() error {
			return SetupVPN(services.EdgeVPNDefaultInstance, cfg.APIAddress, "/", true, prvConfig)
		})
		if err != nil {
			return ErrorEvent("Failed setup: %s", err.Error())
		}
		return pluggable.EventResponse{}
	}

	if tokenNotDefined {
		return ErrorEvent("No network token provided, or kubernetes distribution (k3s, k0s) block configured. Exiting")
	}

	// We might still want a VPN, but not to route traffic into
	if prvConfig.P2P.VPNNeedsCreation() {
		logger.Info("Configuring VPN")
		if err := SetupVPN(services.EdgeVPNDefaultInstance, cfg.APIAddress, "/", true, prvConfig); err != nil {
			return ErrorEvent("Failed setup VPN: %s", err.Error())
		}
	} else { // We need at least the API to co-ordinate
		logger.Info("Configuring API")
		if err := SetupAPI(cfg.APIAddress, "/", true, prvConfig); err != nil {
			return ErrorEvent("Failed setup VPN: %s", err.Error())
		}
	}

	networkID := "kairos"

	if p2pBlockDefined && prvConfig.P2P.NetworkID != "" {
		networkID = prvConfig.P2P.NetworkID
	}

	cc := service.NewClient(
		networkID,
		edgeVPNClient.NewClient(edgeVPNClient.WithHost(cfg.APIAddress)))

	nodeOpts := []service.Option{
		service.WithMinNodes(prvConfig.P2P.MinimumNodes),
		service.WithLogger(logger),
		service.WithClient(cc),
		service.WithUUID(machine.UUID()),
		service.WithStateDir("/usr/local/.kairos/state"),
		service.WithNetworkToken(prvConfig.P2P.NetworkToken),
		service.WithPersistentRoles("auto"),
		service.WithRoles(
			service.RoleKey{
				Role:        "master",
				RoleHandler: p2p.Master(c, prvConfig, false, false, "master"),
			},
			service.RoleKey{
				Role:        "master/clusterinit",
				RoleHandler: p2p.Master(c, prvConfig, true, true, "master/clusterinit"),
			},
			service.RoleKey{
				Role:        "master/ha",
				RoleHandler: p2p.Master(c, prvConfig, false, true, "master/ha"),
			},
			service.RoleKey{
				Role:        "worker",
				RoleHandler: p2p.Worker(c, prvConfig),
			},
			service.RoleKey{
				Role:        "auto",
				RoleHandler: role.Auto(c, prvConfig),
			},
		),
	}

	// Optionally set up a specific node role if the user has defined so
	if prvConfig.P2P.Role != "" {
		nodeOpts = append(nodeOpts, service.WithDefaultRoles(prvConfig.P2P.Role))
	}

	k, err := service.NewNode(nodeOpts...)
	if err != nil {
		return ErrorEvent("Failed creating node: %s", err.Error())
	}
	err = k.Start(context.Background())
	if err != nil {
		return ErrorEvent("Failed start: %s", err.Error())
	}

	return pluggable.EventResponse{
		State: "",
		Data:  "",
		Error: "shouldn't return here",
	}
}

func oneTimeBootstrap(l types.KairosLogger, c *providerConfig.Config, vpnSetupFN func() error) error {
	var err error
	if role.SentinelExist() {
		l.Info("Sentinel exists, nothing to do. exiting.")
		return nil
	}
	l.Info("One time bootstrap starting")

	var svc machine.Service
	var svcName, svcRole, envFile, binPath, args string
	var svcEnv map[string]string

	if !c.IsAKubernetesDistributionEnabled() {
		l.Info("No Kubernetes configuration found, skipping bootstrap.")
		return nil
	}

	svcName = c.K8sServiceName()
	svcRole = c.K8sNodeRole()
	svcEnv = c.K8sEnv()
	args = strings.Join(c.K8sArgs(), " ")

	if c.IsK3sDistributionEnabled() {
		envFile = machine.K3sEnvUnit(svcName)
		binPath = utils.K3sBin()
	}

	if c.IsK0sDistributionEnabled() {
		envFile = machine.K0sEnvUnit(svcName)
		binPath = utils.K0sBin()
	}

	if binPath == "" {
		l.Errorf("no %s binary fouund", svcName)
		return fmt.Errorf("no %s binary found", svcName)
	}

	if err := utils.WriteEnv(envFile, svcEnv); err != nil {
		l.Errorf("Failed to write %s env file: %s", svcName, err.Error())
		return err
	}

	// Initialize the service based on the system's init system
	if utils.IsOpenRCBased() {
		svc, err = openrc.NewService(openrc.WithName(svcName))
	} else {
		svc, err = systemd.NewService(systemd.WithName(svcName))
	}

	if err != nil {
		l.Errorf("Failed to instantiate service: %s", err.Error())
		return err
	}
	if svc == nil {
		return fmt.Errorf("could not detect OS")
	}

	// Override the service command and start it
	if err := svc.OverrideCmd(fmt.Sprintf("%s %s %s", binPath, svcRole, args)); err != nil {
		l.Errorf("Failed to override service command: %s", err.Error())
		return err
	}
	if err := svc.Start(); err != nil {
		l.Errorf("Failed to start service: %s", err.Error())
		return err
	}

	// When this fails, it doesn't produce an error!
	if err := svc.Enable(); err != nil {
		l.Errorf("Failed to enable service: %s", err.Error())
		return err
	}

	// Setup VPN if required
	if c.P2P != nil && c.P2P.VPNNeedsCreation() {
		if err := vpnSetupFN(); err != nil {
			l.Errorf("Failed to setup VPN: %s", err.Error())
			return err
		}
	}

	return role.CreateSentinel()
}
