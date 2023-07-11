package role

import (
	"github.com/kairos-io/kairos-agent/v2/pkg/config"

	providerConfig "github.com/kairos-io/provider-kairos/v2/internal/provider/config"
	utils "github.com/mudler/edgevpn/pkg/utils"

	service "github.com/mudler/edgevpn/api/client/service"
)

func contains(slice []string, elem string) bool {
	for _, s := range slice {
		if elem == s {
			return true
		}
	}
	return false
}

func Auto(cc *config.Config, pconfig *providerConfig.Config) Role { //nolint:revive
	return func(c *service.RoleConfig) error {
		advertizing, _ := c.Client.AdvertizingNodes()
		actives, _ := c.Client.ActiveNodes()

		minimumNodes := pconfig.P2P.MinimumNodes
		if minimumNodes == 0 {
			minimumNodes = 2
		}

		c.Logger.Info("Active nodes:", actives)
		c.Logger.Info("Advertizing nodes:", advertizing)

		if len(advertizing) < minimumNodes {
			c.Logger.Info("Not enough nodes")
			return nil
		}

		// first get available nodes
		nodes := advertizing
		shouldBeLeader := utils.Leader(advertizing)

		lead, _ := c.Client.Get("auto", "leader")

		// From now on, only the leader keeps processing
		// TODO: Make this more reliable with consensus
		if shouldBeLeader != c.UUID && lead != c.UUID {
			c.Logger.Infof("<%s> not a leader, leader is '%s', sleeping", c.UUID, shouldBeLeader)
			return nil
		}

		if shouldBeLeader == c.UUID && (lead == "" || !contains(nodes, lead)) {
			if err := c.Client.Set("auto", "leader", c.UUID); err != nil {
				c.Logger.Error(err)
				return err
			}
			c.Logger.Info("Announcing ourselves as leader, backing off")
			return nil
		}

		if lead != c.UUID {
			c.Logger.Info("Backing off, as we are not currently flagged as leader")
			return nil
		}

		return scheduleRoles(nodes, c, cc, pconfig)
	}
}
