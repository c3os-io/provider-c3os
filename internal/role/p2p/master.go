package role

import (
	"encoding/base64"
	"errors"
	"fmt" // nolint
	"os"
	"strings"
	"time"

	"github.com/kairos-io/kairos-agent/v2/pkg/config"
	"github.com/kairos-io/kairos-sdk/machine"
	"github.com/kairos-io/kairos-sdk/utils"
	providerConfig "github.com/kairos-io/provider-kairos/v2/internal/provider/config"
	"github.com/kairos-io/provider-kairos/v2/internal/role"

	service "github.com/mudler/edgevpn/api/client/service"
)

func propagateMasterData(ip string, c *service.RoleConfig, clusterInit, ha bool, role string, pconfig *providerConfig.Config) error {
	defer func() {
		// Avoid polluting the API.
		// The ledger already retries in the background to update the blockchain, but it has
		// a default timeout where it would stop trying afterwards.
		// Each request here would have it's own background announce, so that can become expensive
		// when network is having lot of changes on its way.
		time.Sleep(30 * time.Second)
	}()

	// If we are configured as master, always signal our role
	if err := c.Client.Set("role", c.UUID, role); err != nil {
		c.Logger.Error(err)
		return err
	}

	if ha && !clusterInit {
		return nil
	}

	if pconfig.IsK3sEnabled() {
		tokenB, err := os.ReadFile("/var/lib/rancher/k3s/server/node-token")
		if err != nil {
			c.Logger.Error(err)
			return err
		}

		nodeToken := string(tokenB)
		nodeToken = strings.TrimRight(nodeToken, "\n")
		if nodeToken != "" {
			err := c.Client.Set("nodetoken", "token", nodeToken)
			if err != nil {
				c.Logger.Error(err)
			}
		}

		kubeB, err := os.ReadFile("/etc/rancher/k3s/k3s.yaml")
		if err != nil {
			c.Logger.Error(err)
			return err
		}
		kubeconfig := string(kubeB)
		if kubeconfig != "" {
			err := c.Client.Set("kubeconfig", "master", base64.RawURLEncoding.EncodeToString(kubeB))
			if err != nil {
				c.Logger.Error(err)
			}
		}
	}

	if pconfig.IsK0sEnabled() {
		controllerToken, err := utils.SH(fmt.Sprintf("k0s token create --role=controller")) //nolint:errcheck
		if err != nil {
			c.Logger.Error(err)
		}

		if controllerToken != "" {
			err := c.Client.Set("controllertoken", "token", controllerToken)
			if err != nil {
				c.Logger.Error(err)
			}
		}

		workerToken, err := utils.SH(fmt.Sprintf("k0s token create --role=worker")) //nolint:errcheck
		if err != nil {
			c.Logger.Error(err)
		}
		if workerToken != "" {
			err := c.Client.Set("workertoken", "token", workerToken)
			if err != nil {
				c.Logger.Error(err)
			}
		}

		kubeconfig, err := utils.SH(fmt.Sprintf("k0s config create")) //nolint:errcheck
		if err != nil {
			c.Logger.Error(err)
			return err
		}
		if kubeconfig != "" {
			err := c.Client.Set("kubeconfig", "master", base64.RawURLEncoding.EncodeToString([]byte(kubeconfig)))
			if err != nil {
				c.Logger.Error(err)
			}
		}
	}

	err := c.Client.Set("master", "ip", ip)
	if err != nil {
		c.Logger.Error(err)
	}
	return nil
}

func genArgs(pconfig *providerConfig.Config, ip, ifaceIP string) (args []string) {

	if pconfig.P2P.UseVPNWithKubernetes() {
		args = append(args, "--flannel-iface=edgevpn0")
	}

	if pconfig.KubeVIP.IsEnabled() {
		args = append(args, fmt.Sprintf("--tls-san=%s", ip), fmt.Sprintf("--node-ip=%s", ifaceIP))
	}

	if pconfig.K3s.EmbeddedRegistry {
		args = append(args, "--embedded-registry")
	}

	return
}

func genEnv(ha, clusterInit bool, c *service.Client, pConfig *providerConfig.Config) (env map[string]string) {
	env = make(map[string]string)

	if ha && !clusterInit {
		if pConfig.IsK3sEnabled() {
			nodeToken, _ := c.Get("nodetoken", "token")
			env["K3S_TOKEN"] = nodeToken
		}

		if pConfig.IsK0sEnabled() {
			nodeToken, _ := c.Get("controllertoken", "token")
			env["K0S_TOKEN"] = nodeToken
		}
	}

	if pConfig.IsK3sEnabled() {
		if !pConfig.K3s.ReplaceEnv {
			// Override opts with user-supplied
			for k, v := range pConfig.K3s.Env {
				env[k] = v
			}
		} else {
			env = pConfig.K3s.Env
		}
	}

	if pConfig.IsK0sEnabled() {
		if !pConfig.K0s.ReplaceEnv {
			// Override opts with user-supplied
			for k, v := range pConfig.K0s.Env {
				env[k] = v
			}
		} else {
			env = pConfig.K0s.Env
		}
	}

	return env
}

// we either return the ElasticIP or the IP from the edgevpn interface.
func guessIP(pconfig *providerConfig.Config) string {
	if pconfig.KubeVIP.EIP != "" {
		return pconfig.KubeVIP.EIP
	}
	return utils.GetInterfaceIP("edgevpn0")
}

func waitForMasterHAInfo(c *service.RoleConfig, pconfig *providerConfig.Config) bool {
	var nodeToken string
	if pconfig.IsK3sEnabled() {
		nodeToken, _ = c.Client.Get("nodetoken", "token")
	}
	if pconfig.IsK0sEnabled() {
		nodeToken, _ = c.Client.Get("controllertoken", "token")
	}
	if nodeToken == "" {
		c.Logger.Info("the nodetoken is not there yet..")
		return true
	}
	clusterInitIP, _ := c.Client.Get("master", "ip")
	if clusterInitIP == "" {
		c.Logger.Info("the clusterInitIP is not there yet..")
		return true
	}

	return false
}

func Master(cc *config.Config, pconfig *providerConfig.Config, clusterInit, ha bool, roleName string) role.Role { //nolint:revive
	return func(c *service.RoleConfig) error {

		iface := guessInterface(pconfig)
		ifaceIP := utils.GetInterfaceIP(iface)
		ip := guessIP(pconfig)
		// If we don't have an IP, we sit and wait
		if ip == "" {
			return errors.New("node doesn't have an ip yet")
		}

		if pconfig.P2P.Role != "" {
			// propagate role if we were forced by configuration
			// This unblocks eventual auto instances to try to assign roles
			if err := c.Client.Set("role", c.UUID, pconfig.P2P.Role); err != nil {
				c.Logger.Error(err)
			}
		}

		if role.SentinelExist() {
			c.Logger.Info("Node already configured, backing off")
			return propagateMasterData(ip, c, clusterInit, ha, roleName, pconfig)
		}

		if ha && !clusterInit && waitForMasterHAInfo(c, pconfig) {
			return nil
		}

		env := genEnv(ha, clusterInit, c.Client, pconfig)

		var svcName string
		if pconfig.P2P.Distribution != "" {
			svcName = pconfig.P2P.Distribution
		}

		if pconfig.IsK3sEnabled() {
			svcName = "k3s"
		}

		if pconfig.IsK0sEnabled() {
			svcName = "k0s"
		}

		if svcName == "" {
			c.Logger.Info("No distribution found, defaulting to k3s")
			svcName = "k3s"
		}

		// Configure k8s service to start on edgevpn0
		c.Logger.Info(fmt.Sprintf("Configuring %s", svcName))

		utils.SH(fmt.Sprintf("kairos-agent run-stage provider-kairos.bootstrap.before.%s", roleName)) //nolint:errcheck

		var svc machine.Service
		var err error

		if pconfig.IsK3sEnabled() {
			svc, err = machine.K3s()
		}

		if pconfig.IsK0sEnabled() {
			svc, err = machine.K0s()
		}

		if err != nil {
			return fmt.Errorf("failed to get %s service: %w", svcName, err)
		}

		var envUnit string

		if svcName == "k3s" {
			envUnit = machine.K3sEnvUnit(svcName)
		}

		if svcName == "k0s" {
			envUnit = machine.K0sEnvUnit(svcName)
		}

		if err := utils.WriteEnv(envUnit,
			env,
		); err != nil {
			return fmt.Errorf("failed to write the %s service: %w", svcName, err)
		}

		args := genArgs(pconfig, ip, ifaceIP)
		if pconfig.KubeVIP.IsEnabled() {
			if err := deployKubeVIP(iface, ip, pconfig); err != nil {
				return fmt.Errorf("failed KubeVIP setup: %w", err)
			}
		}

		if pconfig.P2P.Auto.HA.ExternalDB != "" {
			// TODO: does k0s work with external db?
			args = []string{fmt.Sprintf("--datastore-endpoint=%s", pconfig.P2P.Auto.HA.ExternalDB)}
		}

		if ha && !clusterInit {
			clusterInitIP, _ := c.Client.Get("master", "ip")
			// TODO: does k0s work with this endpiont?
			args = append(args, fmt.Sprintf("--server=https://%s:6443", clusterInitIP))
		}

		if pconfig.IsK3sEnabled() {
			args = pconfig.K3s.AppendArgs(args)
		}

		if pconfig.IsK0sEnabled() {
			args = pconfig.K0s.AppendArgs(args)
		}

		if clusterInit && ha && pconfig.P2P.Auto.HA.ExternalDB == "" {
			args = append(args, "--cluster-init")
		}

		var k8sBin string

		if pconfig.IsK3sEnabled() {
			k8sBin = utils.K3sBin()
		}

		if pconfig.IsK0sEnabled() {
			k8sBin = utils.K0sBin()
		}

		if k8sBin == "" {
			return fmt.Errorf("no %s binary found (?)", svcName)
		}

		if err := svc.OverrideCmd(fmt.Sprintf("%s server %s", k8sBin, strings.Join(args, " "))); err != nil {
			return fmt.Errorf("failed to override %s command: %w", svcName, err)
		}

		if err := svc.Start(); err != nil {
			return fmt.Errorf("failed to start %s service: %w", svcName, err)
		}

		if err := svc.Enable(); err != nil {
			return fmt.Errorf("failed to enable %s service: %w", svcName, err)
		}

		if err := propagateMasterData(ip, c, clusterInit, ha, roleName, pconfig); err != nil {
			return fmt.Errorf("failed to propagate master data: %w", err)
		}

		utils.SH(fmt.Sprintf("kairos-agent run-stage provider-kairos.bootstrap.after.%s", roleName)) //nolint:errcheck

		if err := role.CreateSentinel(); err != nil {
			return fmt.Errorf("failed to create sentinel: %w", err)
		}

		return nil
	}
}
