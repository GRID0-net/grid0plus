"""Routes each connection to the right SwitchNet backend by SNI, mirroring
deploy/nginx.generated.conf's server blocks, so mitmdump can stand in for
nginx entirely for one debug capture instead of only intercepting a single
host.
"""

ROUTES = {
    "dauth-lp1.ndas.srv.nintendo.net": 8081,
    "dauth.hac.lp1.ndas.srv.nintendo.net": 8081,
    "dcert-lp1.ndas.srv.nintendo.net": 8081,
    "aauth-lp1.ndas.srv.nintendo.net": 8082,
    "aauth.hac.lp1.ndas.srv.nintendo.net": 8082,
    "acert-lp1.ndas.srv.nintendo.net": 8082,
    "e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com": 8083,
    "m-lp1.baas.nintendo.com": 8083,
    "cdn-image-e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com": 8083,
    "accounts.nintendo.com": 8083,
    "api.accounts.nintendo.com": 8083,
}
# Everything else (ctest, frs, five, op2, dragons, scsi, penne, npns, beach,
# service-status, znc, ...) goes to stubs, matching nginx's own "stubs" block.
DEFAULT_PORT = 8084


def server_connect(data):
    sni = data.client.sni or ""
    port = ROUTES.get(sni, DEFAULT_PORT)
    data.server.address = ("127.0.0.1", port)
