# OpenWRT feed index — add packages from subdirectories

.PHONY: all clean

all:
	@echo "WDTT OpenWRT feed. Add to OpenWrt:"
	@echo "  echo 'src-link wdtt $$(pwd)' >> feeds.conf.default"
	@echo "  ./scripts/feeds update wdtt && ./scripts/feeds install -a -p wdtt"

clean:
	@echo "Nothing to clean in feed root."
