"""Prints the SNI of every TLS ClientHello, even ones whose handshake later
fails -- reverse mode's default logging never shows this, and a failed
handshake produces no flow entry at all, so this is the only way to see which
hostname a silently-dying connection was actually for.
"""


def tls_clienthello(data):
    print(f">>> ClientHello SNI: {data.client_hello.sni!r}")
