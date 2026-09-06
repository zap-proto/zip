# SPDX-License-Identifier: BSD-3-Clause-Eco
"""The schema, its names, and its ledger."""

import unittest
from dataclasses import dataclass
from typing import Annotated, Any

import zip
from zip import name as nm
from zip import wire


@dataclass
class Ask:
    """What is being asked for."""

    what: Annotated[str, "The thing itself."]


@dataclass
class Told:
    when: zip.u64


def service(*ops):
    """An app carrying one op per (method, path, in, out) given."""
    app = zip.App("svc")
    for method, path, req, rep in ops:
        def fn(x: req) -> rep:
            """Says something."""

        getattr(app, method.lower())(path)(fn)
    return app


class Names(unittest.TestCase):
    def test_the_leading_v1_is_dropped_and_a_parameter_says_by(self):
        self.assertEqual(nm.op("GET", "/v1/orders/{id}"), "get_orders_by_id")
        self.assertEqual(nm.op("GET", "/v2/orders"), "get_v2_orders")

    def test_a_hyphen_is_kept_so_two_addresses_stay_apart(self):
        self.assertNotEqual(nm.op("GET", "/v1/pricing-policy"), nm.op("GET", "/v1/pricing/policy"))

    def test_a_name_the_lexer_cannot_spell_is_recorded_not_folded_away(self):
        s = zip.schema("svc", service(("GET", "/v1/pricing-policy", Ask, Told)))
        self.assertEqual([(r.op, r.method) for r in s.renamed],
                         [("get_pricing-policy", "get_pricing_policy")])
        self.assertIn("get_pricing_policy(req: Ask)", str(s))

    def test_a_declaration_named_as_a_keyword_is_spelled_apart_from_it(self):
        self.assertEqual(nm.idl("text"), "text_")
        self.assertEqual(nm.idl("list"), "list_")
        self.assertEqual(nm.idl("Text"), "Text")

    def test_two_declarations_of_one_name_are_told_apart_by_their_module(self):
        @dataclass
        class Told:  # noqa: F811 — deliberately the same name as the module's
            other: zip.u32

        s = zip.schema("svc", service(
            ("GET", "/v1/a", Ask, globals()["Told"]),
            ("GET", "/v1/b", Ask, Told)))
        self.assertEqual([x.name for x in s.structs], ["Ask", "Told", "test_zap_Told"])


class Ledger(unittest.TestCase):
    def test_an_op_whose_field_names_no_type_is_absent_and_named(self):
        @dataclass
        class Loose:
            body: Any

        s = zip.schema("svc", service(("POST", "/v1/note", Loose, Told)))
        self.assertEqual(s.ops(), 0)
        self.assertEqual([(g.op, g.field, g.cause) for g in s.gaps],
                         [("post_note", "Loose.body", wire.ANY)])
        self.assertEqual(s.blocked(), 1)
        self.assertNotIn("struct Loose", str(s))
        self.assertIn("# blocked (1)", str(s))

    def test_a_nested_value_crosses_and_arrives_without_its_name(self):
        @dataclass
        class Holds:
            one: Ask

        s = zip.schema("svc", service(("POST", "/v1/h", Holds, Told)))
        self.assertEqual([(o.struct, o.field, o.py, o.lst) for o in s.opaque],
                         [("Holds", "one", "Ask", False)])
        self.assertIn("one bytes @0", str(s))

    def test_inline_bytes_are_stated_and_need_a_generated_codec(self):
        @dataclass
        class Keyed:
            key: Annotated[bytes, zip.fixed(32)]

        s = zip.schema("svc", service(("POST", "/v1/k", Keyed, Told)))
        self.assertEqual([(c.struct, c.field, c.type) for c in s.coded],
                         [("Keyed", "key", "bytes_fixed[32]")])

    def test_a_value_that_does_not_cross_at_all_is_the_loudest_line(self):
        @dataclass
        class Nothing:
            pass

        @dataclass
        class Carries:
            n: Nothing

        s = zip.schema("svc", service(("POST", "/v1/c", Carries, Told)))
        self.assertEqual([(d.struct, d.field, d.cause) for d in s.dropped],
                         [("Carries", "n", "empty message")])
        self.assertIn("# dropped (1)", str(s))

    def test_a_schema_that_carries_everything_has_no_ledger(self):
        self.assertNotIn("#  ", str(zip.schema("svc", service(("GET", "/v1/a", Ask, Told)))))


class Text(unittest.TestCase):
    def test_the_prose_sits_where_the_thing_it_describes_is_declared(self):
        s = str(zip.schema("svc", service(("GET", "/v1/a", Ask, Told))))
        self.assertIn("# What is being asked for.\nstruct Ask {", s)
        self.assertIn("    # The thing itself.\n    what text @0", s)
        self.assertIn("    # Says something.\n    get_a(req: Ask)", s)

    def test_a_direction_that_carries_nothing_is_an_empty_parameter_list(self):
        @dataclass
        class Void:
            pass

        s = str(zip.schema("svc", service(("GET", "/v1/a", Void, Told),
                                          ("GET", "/v1/b", Ask, Void))))
        self.assertIn("get_a() returns (rep: Told)", s)
        self.assertIn("    get_b(req: Ask)\n", s)

    def test_the_same_program_writes_the_same_bytes(self):
        one = str(zip.schema("svc", service(("GET", "/v1/b", Ask, Told),
                                            ("GET", "/v1/a", Told, Ask))))
        two = str(zip.schema("svc", service(("GET", "/v1/a", Told, Ask),
                                            ("GET", "/v1/b", Ask, Told))))
        self.assertEqual(one, two)


if __name__ == "__main__":
    unittest.main()
