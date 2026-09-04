# Packages the already cross-compiled tunnelkit binary (from CI) into a .ipk.
# Copy the binary for your $GOARCH into this folder as ./tunnelkit-client before building.
include $(TOPDIR)/rules.mk

PKG_NAME:=tunnelkit
PKG_VERSION:=0.1.0
PKG_RELEASE:=1
PKG_MAINTAINER:=tunnelkit <maintainer@example.com>
PKG_LICENSE:=MIT

include $(INCLUDE_DIR)/package.mk

define Package/tunnelkit
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Tunnelkit reverse SSH tunnel agent
  DEPENDS:=+openssh-client
endef

define Package/tunnelkit/description
  Reverse SSH tunnel agent with a stats endpoint and speedtest.
endef

# The binary is already compiled; we do not build inside the SDK.
define Build/Compile
endef

define Package/tunnelkit/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) ./tunnelkit-client $(1)/usr/bin/tunnelkit-client
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/tunnelkit.init $(1)/etc/init.d/tunnelkit
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/tunnelkit.config $(1)/etc/config/tunnelkit
endef

$(eval $(call BuildPackage,tunnelkit))
