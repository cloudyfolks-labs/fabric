package dnsnameresolver

import (
	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
)

const (
	pluginName = "dnsnameresolver"
)

var log = clog.NewWithPlugin(pluginName)

func init() { plugin.Register(pluginName, setup) }

func setup(c *caddy.Controller) error {
	resolver, err := resolverParse(c)
	if err != nil {
		return plugin.Error(pluginName, err)
	}

	onStart, onShut, err := resolver.initPlugin()
	if err != nil {
		return plugin.Error(pluginName, err)
	}

	c.OnStartup(onStart)
	c.OnShutdown(onShut)

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		resolver.Next = next
		return resolver
	})

	return nil
}

func resolverParse(c *caddy.Controller) (*DNSNameResolver, error) {
	resolver := New()

	for c.Next() {
		// There shouldn't be any more arguments.
		if len(c.RemainingArgs()) != 0 {
			return nil, c.ArgErr()
		}

		// No configuration parameters are supported
		if c.NextBlock() {
			return nil, c.Errf("unknown property %q", c.Val())
		}
	}
	return resolver, nil
}
