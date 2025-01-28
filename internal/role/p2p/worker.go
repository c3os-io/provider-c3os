package role

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kairos-io/kairos-agent/v2/pkg/config"
	"github.com/kairos-io/kairos-sdk/machine"
	"github.com/kairos-io/kairos-sdk/utils"

	providerConfig "github.com/kairos-io/provider-kairos/v2/internal/provider/config"
	"github.com/kairos-io/provider-kairos/v2/internal/role"
	service "github.com/mudler/edgevpn/api/client/service"
)

func Worker(cc *config.Config, pconfig *providerConfig.Config) role.Role { //nolint:revive
	return func(c *service.RoleConfig) error {

		if pconfig.P2P.Role != "" {
			// propagate role if we were forced by configuration
			// This unblocks eventual auto instances to try to assign roles
			if err := c.Client.Set("role", c.UUID, pconfig.P2P.Role); err != nil {
				return err
			}
		}

		if role.SentinelExist() {
			c.Logger.Info("Node already configured, backing off")
			return nil
		}

		masterIP, _ := c.Client.Get("master", "ip")
		if masterIP == "" {
			c.Logger.Info("MasterIP not there still..")
			return nil
		}

		var svc machine.Service
		var err error
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

		if svcName == "k3s" {
			nodeToken, _ := c.Client.Get("nodetoken", "token")
			if nodeToken == "" {
				c.Logger.Info("node token not there still..")
				return nil
			}

			nodeToken = strings.TrimRight(nodeToken, "\n")

			svc, err = machine.K3sAgent()
			if err != nil {
				return err
			}

			k3sConfig := providerConfig.K3s{}
			if pconfig.K3sAgent.Enabled {
				k3sConfig = pconfig.K3sAgent
			}

			env := map[string]string{
				"K3S_URL":   fmt.Sprintf("https://%s:6443", masterIP),
				"K3S_TOKEN": nodeToken,
			}

			if !k3sConfig.ReplaceEnv {
				// Override opts with user-supplied
				for k, v := range k3sConfig.Env {
					env[k] = v
				}
			} else {
				env = k3sConfig.Env
			}

			args := []string{
				"--with-node-id",
			}

			if pconfig.P2P.UseVPNWithKubernetes() {
				ip := utils.GetInterfaceIP("edgevpn0")
				if ip == "" {
					return errors.New("node doesn't have an ip yet")
				}
				args = append(args,
					fmt.Sprintf("--node-ip %s", ip),
					"--flannel-iface=edgevpn0")
			} else {
				iface := guessInterface(pconfig)
				ip := utils.GetInterfaceIP(iface)
				args = append(args,
					fmt.Sprintf("--node-ip %s", ip))
			}

			c.Logger.Info("Configuring k3s-agent", masterIP, nodeToken, args)

			utils.SH(fmt.Sprintf("kairos-agent run-stage provider-kairos.bootstrap.before.%s", "worker")) //nolint:errcheck

			// Setup systemd unit and starts it
			if err := utils.WriteEnv(machine.K3sEnvUnit("k3s-agent"),
				env,
			); err != nil {
				return err
			}

			if k3sConfig.ReplaceArgs {
				args = k3sConfig.Args
			} else {
				args = append(args, k3sConfig.Args...)
			}

			k3sbin := utils.K3sBin()
			if k3sbin == "" {
				return fmt.Errorf("no k3s binary found (?)")
			}
			if err := svc.OverrideCmd(fmt.Sprintf("%s agent %s", k3sbin, strings.Join(args, " "))); err != nil {
				return err
			}
		}

		if svcName == "k0s" {
			nodeToken, _ := c.Client.Get("workertoken", "token")
			if nodeToken == "" {
				c.Logger.Info("node token not there still..")
				return nil
			}
			if err := os.WriteFile("/etc/k0s/token", []byte(nodeToken), 0644); err != nil {
				return err
			}

			svc, err = machine.K0sWorker()
			if err != nil {
				return err
			}

			c.Logger.Info("Configuring k0sworker", masterIP, nodeToken)

			utils.SH(fmt.Sprintf("kairos-agent run-stage provider-kairos.bootstrap.before.%s", "worker")) //nolint:errcheck

			k0sConfig := pconfig.K0sWorker
			args := []string{"--token-file /etc/k0s/token"}

			if k0sConfig.ReplaceArgs {
				args = k0sConfig.Args
			} else {
				args = append(args, k0sConfig.Args...)
			}

			k0sbin := utils.K0sBin()
			if k0sbin == "" {
				return fmt.Errorf("no k0s binary found (?)")
			}
			if err := svc.OverrideCmd(fmt.Sprintf("%s worker %s", k0sbin, strings.Join(args, " "))); err != nil {
				return err
			}
		}

		if err := svc.Start(); err != nil {
			return err
		}

		if err := svc.Enable(); err != nil {
			return err
		}

		utils.SH(fmt.Sprintf("kairos-agent run-stage provider-kairos.bootstrap.after.%s", "worker")) //nolint:errcheck

		return role.CreateSentinel()
	}
}
