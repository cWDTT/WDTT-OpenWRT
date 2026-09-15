include $(TOPDIR)/rules.mk

PKG_NAME:=wdtt-client
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

PKG_MAINTAINER:=WDTT OpenWRT <wdtt@openwrt.local>
PKG_LICENSE:=GPL-3.0-only
PKG_LICENSE_FILES:=LICENSE

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/wdtt-client
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=WDTT VPN client (WireGuard over VK TURN)
  URL:=https://github.com/cWDTT/WDTT-OpenWRT
  DEPENDS:=+wireguard-tools +kmod-wireguard +kmod-tun +ca-bundle +nftables +kmod-nft-core +dnsmasq-full +curl
endef

define Package/wdtt-client/description
  WDTT — WireGuard-туннель через DTLS-медиарелей VK TURN-серверов.
  Клиент для OpenWRT: Go-ядро транспорта + WireGuard-интерфейс.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) ./src/* $(PKG_BUILD_DIR)/
endef

define Build/Compile
	$(eval GO_ARCH:=$(if $(filter aarch64,$(ARCH)),arm64,$(if $(filter armeb,$(ARCH)),arm,$(ARCH))))
	( \
		cd $(PKG_BUILD_DIR) && \
		GOOS=linux GOARCH=$(GO_ARCH) \
		CGO_ENABLED=0 \
		$(HOST_GO) build -trimpath \
			-ldflags="-s -w -X main.version=$(PKG_VERSION)" \
			-o $(PKG_BUILD_DIR)/wdttd ./cmd/wdttd \
	)
endef

define Package/wdtt-client/install
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/wdttd $(1)/usr/sbin/wdttd
	$(INSTALL_DIR) $(1)/usr/libexec/wdtt
	$(INSTALL_BIN) ./files/wdtt-routing $(1)/usr/libexec/wdtt/routing
	$(INSTALL_BIN) ./files/wdtt-datapath $(1)/usr/libexec/wdtt/datapath
	$(INSTALL_BIN) ./files/wdtt-keepalive $(1)/usr/libexec/wdtt/keepalive
	$(INSTALL_BIN) ./files/wdtt-firewall-refresh $(1)/usr/libexec/wdtt/firewall-refresh
	$(INSTALL_BIN) ./files/wdtt-fix-config $(1)/usr/libexec/wdtt/fix-config
	$(INSTALL_BIN) ./files/wdtt-doctor $(1)/usr/libexec/wdtt/doctor
	$(INSTALL_BIN) ./files/wdtt-full-tunnel $(1)/usr/libexec/wdtt/full-tunnel
	$(INSTALL_BIN) ./files/wdtt-uplink $(1)/usr/libexec/wdtt/uplink
	$(INSTALL_BIN) ./files/wdtt-domain-lib.sh $(1)/usr/libexec/wdtt/domain-lib.sh
	$(INSTALL_DIR) $(1)/var/run/wdtt
endef

$(eval $(call BuildPackage,wdtt-client))
