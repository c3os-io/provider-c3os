// nolint
package mos

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/spectrocloud/peg/matcher"
)

var _ = Describe("k3s upgrade test", Label("upgrade-k8s"), func() {
	var vm VM

	BeforeEach(func() {
		iso := os.Getenv("ISO")
		_, vm = startVM(iso)
		vm.EventuallyConnects(1200)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			gatherLogs(vm)
		}
		vm.Destroy(nil)
	})

	It("installs to disk with custom config", func() {
		By("checking if it has default service active")
		if isFlavor(vm, "alpine") {
			out, _ := vm.Sudo("rc-status")
			Expect(out).Should(ContainSubstring("kairos"))
			Expect(out).Should(ContainSubstring("kairos-agent"))
			out, _ = vm.Sudo("ps aux")
			Expect(out).Should(ContainSubstring("/usr/sbin/crond"))
		} else {
			out, _ := vm.Sudo("systemctl status kairos")
			Expect(out).Should(ContainSubstring("loaded (/etc/systemd/system/kairos.service; enabled; vendor preset: disabled)"))

			out, _ = vm.Sudo("systemctl status logrotate.timer")
			Expect(out).Should(ContainSubstring("active (waiting)"))
		}

		By("copy the config")
		err := vm.Scp("assets/single.yaml", "/tmp/config.yaml", "0770")
		Expect(err).ToNot(HaveOccurred())

		By("find the correct device (qemu vs vbox)")
		device, err := vm.Sudo(`[[ -e /dev/sda ]] && echo "/dev/sda" || echo "/dev/vda"`)
		Expect(err).ToNot(HaveOccurred(), device)

		By("installing")
		out, _ := vm.Sudo(fmt.Sprintf("elemental install --cloud-init /tmp/config.yaml %s", device))
		Expect(out).Should(ContainSubstring("Running after-install hook"))

		out, err = vm.Sudo("sync")
		Expect(err).ToNot(HaveOccurred(), out)

		vm.Reboot()

		By("checking default services are on after first boot")
		if isFlavor(vm, "alpine") {
			Eventually(func() string {
				out, _ := vm.Sudo("rc-status")
				return out
			}, 30*time.Second, 10*time.Second).Should(And(
				ContainSubstring("kairos")),
				ContainSubstring("kairos-agent"))
		} else {
			Eventually(func() string {
				out, _ := vm.Sudo("systemctl status kairos-agent")
				return out
			}, 30*time.Second, 10*time.Second).Should(ContainSubstring(
				"loaded (/etc/systemd/system/kairos-agent.service; enabled; vendor preset: disabled)"))

			Eventually(func() string {
				out, _ := vm.Sudo("systemctl status systemd-timesyncd")
				return out
			}, 30*time.Second, 10*time.Second).Should(ContainSubstring(
				"loaded (/usr/lib/systemd/system/systemd-timesyncd.service; enabled; vendor preset: disabled)"))
		}

		By("checking if it has a working kubeconfig")
		Eventually(func() string {
			var out string
			if isFlavor(vm, "alpine") {
				out, _ = vm.Sudo("cat /var/log/kairos/agent.log;cat /var/log/kairos-agent.log")
			} else {
				out, _ = vm.Sudo("systemctl status kairos-agent")
			}
			return out
		}, 900*time.Second, 10*time.Second).Should(ContainSubstring("One time bootstrap starting"))

		Eventually(func() string {
			out, _ := vm.Sudo("cat /var/log/kairos/agent-provider.log")
			return out
		}, 900*time.Second, 10*time.Second).Should(Or(ContainSubstring("One time bootstrap starting"), ContainSubstring("Sentinel exists")))

		Eventually(func() string {
			out, _ := vm.Sudo("cat /etc/rancher/k3s/k3s.yaml")
			return out
		}, 900*time.Second, 10*time.Second).Should(ContainSubstring("https:"))

		By("checking if logs are rotated")
		out, err = vm.Sudo("logrotate -vf /etc/logrotate.d/kairos")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("log needs rotating"))
		_, err = vm.Sudo("ls /var/log/kairos/agent-provider.log.1.gz")
		Expect(err).ToNot(HaveOccurred())

		By("wait system-upgrade-controller")
		Eventually(func() string {
			out, _ := kubectl(vm, "get pods -A")
			return out
		}, 900*time.Second, 10*time.Second).Should(ContainSubstring("system-upgrade-controller"))

		By("applying upgrade plan")
		err = vm.Scp("assets/suc.yaml", "./suc.yaml", "0770")
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() string {
			out, _ := kubectl(vm, "apply -f suc.yaml")
			return out
		}, 900*time.Second, 10*time.Second).Should(ContainSubstring("unchanged"))

		Eventually(func() string {
			out, _ = kubectl(vm, "get pods -A")
			return out
		}, 900*time.Second, 10*time.Second).Should(ContainSubstring("apply-os-upgrade-on-"), out)

		Eventually(func() string {
			out, _ = kubectl(vm, "get pods -A")
			version, _ := vm.Sudo(getVersionCmd)
			return version
		}, 30*time.Minute, 10*time.Second).Should(ContainSubstring("v"), out)
	})
})
