package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	logging "github.com/ipfs/go-log"
	edgeVPNClient "github.com/mudler/edgevpn/api/client"
	"go.uber.org/zap"

	"github.com/kairos-io/kairos-sdk/bus"
	"github.com/kairos-io/kairos-sdk/machine"
	"github.com/kairos-io/kairos-sdk/machine/openrc"
	"github.com/kairos-io/kairos-sdk/machine/systemd"
	"github.com/kairos-io/kairos-sdk/utils"
	providerConfig "github.com/kairos-io/provider-kairos/v2/internal/provider/config"
	"github.com/kairos-io/provider-kairos/v2/internal/role"
	p2p "github.com/kairos-io/provider-kairos/v2/internal/role/p2p"

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
	providerConfig := &providerConfig.Config{}
	err = config.FromString(cfg.Config, c)
	if err != nil {
		return ErrorEvent("Failed reading JSON input: %s input '%s'", err.Error(), cfg.Config)
	}

	err = config.FromString(cfg.Config, providerConfig)
	if err != nil {
		return ErrorEvent("Failed reading JSON input: %s input '%s'", err.Error(), cfg.Config)
	}
	// TODO: this belong to a systemd service that is started instead

	p2pBlockDefined := providerConfig.P2P != nil
	tokenNotDefined := ((p2pBlockDefined && providerConfig.P2P.NetworkToken == "") || !p2pBlockDefined)
	skipAuto := (p2pBlockDefined && !providerConfig.P2P.Auto.IsEnabled())

	if providerConfig.P2P == nil && !providerConfig.K3s.Enabled && !providerConfig.K3sAgent.Enabled {
		return pluggable.EventResponse{State: fmt.Sprintf("no kairos or k3s configuration. nothing to do: %s", cfg.Config)}
	}

	utils.SH("elemental run-stage kairos-agent.bootstrap")    //nolint:errcheck
	bus.RunHookScript("/usr/bin/kairos-agent.bootstrap.hook") //nolint:errcheck

	logLevel := "debug"

	if p2pBlockDefined && providerConfig.P2P.LogLevel != "" {
		logLevel = providerConfig.P2P.LogLevel
	}

	lvl, err := logging.LevelFromString(logLevel)
	if err != nil {
		return ErrorEvent("Failed setup logger: %s", err.Error())
	}

	// TODO: Fixup Logging to file
	loggerCfg := zap.NewProductionConfig()
	loggerCfg.OutputPaths = []string{
		cfg.Logfile,
	}
	logger, err := loggerCfg.Build()
	if err != nil {
		return ErrorEvent("Failed setup logger: %s", err.Error())
	}

	logging.SetAllLoggers(lvl)

	log := &logging.ZapEventLogger{SugaredLogger: *logger.Sugar()}

	// Do onetimebootstrap if K3s or K3s-agent are enabled.
	// Those blocks are not required to be enabled in case of a kairos
	// full automated setup. Otherwise, they must be explicitly enabled.
	if (tokenNotDefined && (providerConfig.K3s.Enabled || providerConfig.K3sAgent.Enabled)) || skipAuto {
		err := oneTimeBootstrap(log, providerConfig, func() error {
			return SetupVPN(services.EdgeVPNDefaultInstance, cfg.APIAddress, "/", true, providerConfig)
		})
		if err != nil {
			return ErrorEvent("Failed setup: %s", err.Error())
		}
		return pluggable.EventResponse{}
	}

	if tokenNotDefined {
		return ErrorEvent("No network token provided, or `k3s` block configured. Exiting")
	}

	// We might still want a VPN, but not to route traffic into
	if providerConfig.P2P.VPNNeedsCreation() {
		logger.Info("Configuring VPN")
		if err := SetupVPN(services.EdgeVPNDefaultInstance, cfg.APIAddress, "/", true, providerConfig); err != nil {
			return ErrorEvent("Failed setup VPN: %s", err.Error())
		}
	} else { // We need at least the API to co-ordinate
		logger.Info("Configuring API")
		if err := SetupAPI(cfg.APIAddress, "/", true, providerConfig); err != nil {
			return ErrorEvent("Failed setup VPN: %s", err.Error())
		}
	}

	networkID := "kairos"

	if p2pBlockDefined && providerConfig.P2P.NetworkID != "" {
		networkID = providerConfig.P2P.NetworkID
	}

	cc := service.NewClient(
		networkID,
		edgeVPNClient.NewClient(edgeVPNClient.WithHost(cfg.APIAddress)))

	nodeOpts := []service.Option{
		service.WithMinNodes(providerConfig.P2P.MinimumNodes),
		service.WithLogger(log),
		service.WithClient(cc),
		service.WithUUID(machine.UUID()),
		service.WithStateDir("/usr/local/.kairos/state"),
		service.WithNetworkToken(providerConfig.P2P.NetworkToken),
		service.WithPersistentRoles("auto"),
		service.WithRoles(
			service.RoleKey{
				Role:        "master",
				RoleHandler: p2p.Master(c, providerConfig, false, false, "master"),
			},
			service.RoleKey{
				Role:        "master/clusterinit",
				RoleHandler: p2p.Master(c, providerConfig, true, true, "master/clusterinit"),
			},
			service.RoleKey{
				Role:        "master/ha",
				RoleHandler: p2p.Master(c, providerConfig, false, true, "master/ha"),
			},
			service.RoleKey{
				Role:        "worker",
				RoleHandler: p2p.Worker(c, providerConfig),
			},
			service.RoleKey{
				Role:        "auto",
				RoleHandler: role.Auto(c, providerConfig),
			},
		),
	}

	// Optionally set up a specific node role if the user has defined so
	if providerConfig.P2P.Role != "" {
		nodeOpts = append(nodeOpts, service.WithDefaultRoles(providerConfig.P2P.Role))
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

func oneTimeBootstrap(l logging.StandardLogger, c *providerConfig.Config, vpnSetupFN func() error) error {
	if role.SentinelExist() {
		l.Info("Sentinel exists, nothing to do. exiting.")
		return nil
	}
	l.Info("One time bootstrap starting")

	var svc machine.Service
	k3sConfig := providerConfig.K3s{}
	svcName := "k3s"
	svcRole := "server"

	if c.K3s.Enabled {
		k3sConfig = c.K3s
	} else if c.K3sAgent.Enabled {
		k3sConfig = c.K3sAgent
		svcName = "k3s-agent"
		svcRole = "agent"
	}

	if utils.IsOpenRCBased() {
		svc, _ = openrc.NewService(
			openrc.WithName(svcName),
		)
	} else {
		svc, _ = systemd.NewService(
			systemd.WithName(svcName),
		)
	}

	envFile := machine.K3sEnvUnit(svcName)
	if svc == nil {
		return fmt.Errorf("could not detect OS")
	}

	// Setup systemd unit and starts it
	if err := utils.WriteEnv(envFile,
		k3sConfig.Env,
	); err != nil {
		return err
	}

	k3sbin := utils.K3sBin()
	if k3sbin == "" {
		return fmt.Errorf("no k3s binary found (?)")
	}
	if err := svc.OverrideCmd(fmt.Sprintf("%s %s %s", k3sbin, svcRole, strings.Join(k3sConfig.Args, " "))); err != nil {
		return err
	}

	if err := svc.Start(); err != nil {
		return err
	}

	if err := svc.Enable(); err != nil {
		return err
	}

	if c.P2P != nil && c.P2P.VPNNeedsCreation() {
		if err := vpnSetupFN(); err != nil {
			return err
		}
	}

	return role.CreateSentinel()
}
