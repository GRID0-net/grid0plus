# Schedule/SelectVsParams response version

In both working Nextendo hall captures, all seven `SelectVsParams` requests omit an ETag and each response carries the same nonempty, quoted 24-character ETag. The value is a server version, not an echo of a request field. No captured token or literal ETag is reproduced here.

SwitchNet previously set the response ETag to `req.GetEtag()`, which answered empty for every observed request. It now derives a stable quoted version from its own target and slot-based `ParamsSetId`. The format and stability match the captured behavior; the bytes are SwitchNet's own. This is a confirmed response mismatch, but whether it contributes to the hall communication error is not yet established by hardware.
