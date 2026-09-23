# Compatibility

What each operator release was verified with, and the order the four repositories ship in. The
operator is what installs the set: `values.yaml` pins the MCP server (`mcpServer.tag`), the bots
(`bots.fabricTag`, `bots.azaleaTag`) and the asset fetcher (`bots.assetFetcherImage`), and
`make verify` ran the join loop against exactly these. A row is added when a release moves any
of them.

The catalogue is the tool schema the bots were generated from; a bot built from another one has
its mismatched tools disabled at handshake rather than the link refused. Minecraft is the one
version the bot images are built for, carried in their tags as `-mc<version>`.

| operator | mcp-server | catalogue | bot-fabric | bot-azalea | mc-assets | Minecraft | release order |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0.21.0 | 0.71.0 | 5.10.0 | 0.75.0 | 0.26.0 | 0.75.0 | 26.1.2 | mcp-server, then the bots, then the operator |
| 0.20.0 | 0.68.0 | 5.6.0 | 0.74.0 | 0.26.0 | 0.74.0 | 26.1.2 | mcp-server, then the bots, then the operator |
| 0.19.0 | 0.67.0 | 5.5.0 | 0.73.1 | 0.25.1 | 0.73.1 | 26.1.2 | mcp-server, then the operator; the bots are unchanged |
| 0.18.1 | 0.66.0 | 5.5.0 | 0.73.1 | 0.25.1 | 0.73.1 | 26.1.2 | the bots, then the operator; mcp-server is unchanged |
| 0.18.0 | 0.66.0 | 5.5.0 | 0.73.0 | 0.25.0 | 0.73.0 | 26.1.2 | mcp-server, then the bots, then the operator |
| 0.17.5 | 0.65.9 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.17.4 | 0.65.7 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.17.3 | 0.65.6 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.17.2 | 0.65.2 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.17.1 | 0.65.1 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.17.0 | 0.65.0 | 5.4.0 | 0.72.0 | 0.24.0 | 0.72.0 | 26.1.2 | the bots, then mcp-server, then the operator |
| 0.16.1 | 0.64.1 | 5.3.0 | 0.71.0 | 0.23.0 | 0.71.0 | 26.1.2 | mcp-server, then the operator; the bots are unchanged |
| 0.16.0 | 0.64.0 | 5.3.0 | 0.71.0 | 0.23.0 | 0.71.0 | 26.1.2 | mcp-server, then the operator; the bots are unchanged |
| 0.15.0 | 0.63.0 | 5.2.0 | 0.71.0 | 0.23.0 | 0.71.0 | 26.1.2 | mcp-server, then the bots, then the operator |
| 0.14.0 | 0.62.0 | 5.1.0 | 0.70.0 | 0.22.0 | 0.70.0 | 26.1.2 | the bots, then mcp-server, then the operator |

## Why that order

A change that touches the wire between the three lands tolerant on both sides before the operator
turns it on: the MCP server ignores what it was not configured for, a bot sends only what it was
given, and only an operator release puts the two together. The link token is the example: a server
with no `BOT_LINK_TOKEN` ignores the field, a bot with none set sends none, and the operator hands
the token to both. At every step the pair already deployed keeps working, so a rollback is one
release backwards rather than three. The operator is always last, since its chart pins the other
three; between the server and the bots the order is whichever the release's CI gate needs. The
0.14.0 row went bots first: the server's end-to-end suite now runs with a link token and pulls the
bots' `latest` images, so the bots that present one had to be published before the server could
be.

Installing a row is `helm install --version <operator>` and nothing else: the MCP server and bot
tags in the row are the chart's defaults. Overriding `mcpServer.tag` or a profile's tags to
versions from another row is running a set nobody verified.
