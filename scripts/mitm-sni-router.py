"""Routes each connection to the right GRID0+ backend by SNI, mirroring
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
    # scsi (OLSC cloud save) -- deploy/nginx.generated.conf's "scsi" block.
    "storage.hac.lp1.scsi.srv.nintendo.net": 8091,
    "permission.lp1.sata.srv.nintendo.net": 8091,
    # npln_rest -- everything Splatoon 3 talks to before it opens its gRPC
    # channel (vermillion, penne login tickets, the fro-N frontline
    # connection). nginx's own "npln_rest" block.
    "gw.hac.lp1.vermillion.srv.nintendo.net": 8086,
    "val.hac.lp1.penne.srv.nintendo.net": 8086,
    "fro-1.hac.lp1.penne.srv.nintendo.net": 8086,
    "fro-2.hac.lp1.penne.srv.nintendo.net": 8086,
    "fro-3.hac.lp1.penne.srv.nintendo.net": 8086,
    "fro-4.hac.lp1.penne.srv.nintendo.net": 8086,
    # npln -- the gRPC tenant host. nginx's own "npln" block routes this same
    # host to switchnet_gamesync for the Gamesync service specifically and
    # switchnet_npln for everything else; SNI alone cannot make that same
    # split (it does not see the gRPC path), so this sends the whole host to
    # 8085. A capture that needs Gamesync traffic specifically should use
    # port 7575 directly -- see gamesync.npln.nintendo.net below, which is
    # nginx's own answer to the exact same problem (gamesync.go's own
    # comment: "the console reaches gamesync directly on 7575 rather than
    # through the tenant host, so a block on the edge port would never match
    # it").
    "t-dce9377b-lp1.lp1.t.npln.srv.nintendo.net": 8085,
}
# Everything else (ctest, frs, five, op2, dragons, penne's fro-N already
# listed above, npns, beach, service-status, znc, ...) goes to stubs,
# matching nginx's own "stubs" block.
DEFAULT_PORT = 8084

# gamesync.npln.nintendo.net is NOT reached through this port at all -- nginx
# listens for it on its own port, 7575, the same reason given in ROUTES'
# comment above. mitmdump needs a second reverse-mode listener bound to 7575
# to see it; see docs/PACKET_CAPTURE.md's own command for how the two are
# combined in one process. This addon's server_connect only ever sees
# connections mitmdump itself accepted, so nothing here has to special-case
# that port -- whichever listener accepted the connection already fixed
# which of the two mitmdump was told to treat it as.
GAMESYNC_PORT = 8088


def server_connect(data):
    sni = data.client.sni or ""
    port = ROUTES.get(sni, DEFAULT_PORT)
    data.server.address = ("127.0.0.1", port)
