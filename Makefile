VERSION := $(if $(VERSION),$(VERSION),"0.0.0-dev")

binary: commands.go devices.go events.go main.go
	go build -ldflags "-X main.AppVersion=$(VERSION)" -o cbus2mqtt commands.go devices.go events.go main.go

clean:
	rm cbus2mqtt
	rm -rf deb
	rm cbus2mqtt_*.deb

install:
	install -m 755 cbus2mqtt /usr/bin/
	mkdir -p /etc/cbus2mqtt /var/lib/cbus2mqtt
	install -D -m 755 system/cbus2mqtt.init /etc/init.d/cbus2mqtt
	install -D -m 644 system/cbus2mqtt.service /lib/systemd/system/cbus2mqtt.service
	bash system/cbus2mqtt.postinst configure

test:
	go test ./devices
	go test ./core
	bash system/test_deb_install.sh

ARCH := $(if $(ARCH),$(ARCH),$(shell dpkg --print-architecture))
deb: binary
	rm -rf deb
	mkdir -p deb/cbus2mqtt/usr/bin
	mkdir -p deb/cbus2mqtt/etc/cbus2mqtt
	mkdir -p deb/cbus2mqtt/etc/init.d
	mkdir -p deb/cbus2mqtt/lib/systemd/system
	mkdir -p deb/cbus2mqtt/var/lib/cbus2mqtt
	mkdir deb/cbus2mqtt/DEBIAN
	cp deb.control deb/cbus2mqtt/DEBIAN/control
	sed -i'' "s/__version__/$(VERSION)/" deb/cbus2mqtt/DEBIAN/control
	sed -i'' "s/__arch__/$(ARCH)/" deb/cbus2mqtt/DEBIAN/control
	cat deb/cbus2mqtt/DEBIAN/control
	cp cbus2mqtt deb/cbus2mqtt/usr/bin
	install -m 755 system/cbus2mqtt.init deb/cbus2mqtt/etc/init.d/cbus2mqtt
	install -m 644 system/cbus2mqtt.service deb/cbus2mqtt/lib/systemd/system
	install -m 755 system/cbus2mqtt.postinst deb/cbus2mqtt/DEBIAN/postinst
	printf '/etc/init.d/cbus2mqtt\n' > deb/cbus2mqtt/DEBIAN/conffiles
	dpkg-deb -Zgzip --root-owner-group --build deb/cbus2mqtt
