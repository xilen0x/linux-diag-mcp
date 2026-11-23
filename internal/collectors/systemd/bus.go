package systemd

import (
	"context"
	"fmt"
	"os"
	"strconv"

	sd "github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
)

// SystemBusSocket is the only D-Bus endpoint the server talks to. The
// sandbox grants connect(2) on it and nothing else.
const SystemBusSocket = "/run/dbus/system_bus_socket"

// DialSystemBus connects to the system bus at a fixed address.
//
// godbus would otherwise honor DBUS_SYSTEM_BUS_ADDRESS, where "autolaunch:"
// executes dbus-launch and "tcp:" opens a network connection. Only the
// EXTERNAL mechanism is offered: DBUS_COOKIE_SHA1 would read files under
// the home directory.
func DialSystemBus(ctx context.Context) (Conn, error) {
	conn, err := sd.NewConnection(func() (*godbus.Conn, error) {
		c, err := godbus.Dial("unix:path="+SystemBusSocket, godbus.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		auth := []godbus.Auth{godbus.AuthExternal(strconv.Itoa(os.Getuid()))}
		if err := c.Auth(auth); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
		if err := c.Hello(); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("hello: %w", err)
		}
		return c, nil
	})
	if err != nil {
		// Return a nil interface, not a nil *sd.Conn wrapped in one.
		return nil, err
	}
	return conn, nil
}
