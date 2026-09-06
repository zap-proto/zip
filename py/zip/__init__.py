# SPDX-License-Identifier: BSD-3-Clause-Eco
"""zip — a Python service declares its operations, and answers with a .zap schema.

    from dataclasses import dataclass
    from typing import Annotated
    import zip

    @dataclass
    class Order:
        sku: Annotated[str, "What is being bought, by catalogue number."]
        count: zip.u32

    @dataclass
    class Receipt:
        cents: zip.u64

    app = zip.App("orders")

    @app.post("/v1/orders")
    def place(order: Order) -> Receipt:
        '''Place an order. It is filled from stock or refused, never queued.'''
        return Receipt(cents=1200 * order.count)

    print(zip.schema("orders", app))

That schema is the whole output, and every other artifact — the OpenAPI
document, the MCP tool list, the CLI, the SDKs — is generated FROM it by the one
downstream projector, never here. A front end that grew its own OpenAPI emitter
would make a second document out of one service; there is one, and it is shared
with every other language that reaches this same file.
"""

from .app import App, Op, Refused
from .wire import f32, fixed, i8, i16, i32, i64, u8, u16, u32, u64
from .zap import Schema, schema

__all__ = [
    "App", "Op", "Refused", "Schema", "schema", "fixed",
    "u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "f32",
]
