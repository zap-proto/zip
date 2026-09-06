# SPDX-License-Identifier: BSD-3-Clause-Eco
"""The layout, and what cannot be one.

The offsets asserted here are the ones the reader on the other side derives for
the equivalent declaration. They are stated as literals rather than recomputed,
because a test that computed them with the code under test would agree with any
rule at all.
"""

import unittest
from dataclasses import dataclass
from typing import Annotated, Any, Optional, Union

import zip
from zip import wire


@dataclass
class Mixed:
    ok: bool
    n: zip.u32
    big: int
    who: str
    tags: list[str]
    id: Annotated[bytes, zip.fixed(20)]
    seq: zip.u16


class Layout(unittest.TestCase):
    def test_each_slot_aligns_to_its_own_width(self):
        sh = wire.shape(Mixed)
        self.assertEqual(
            [(s.name, s.type, s.offset) for s in sh.slots],
            [("ok", "bool", 0), ("n", "u32", 4), ("big", "i64", 8), ("who", "text", 16),
             ("tags", "list<text>", 24), ("id", "bytes_fixed[20]", 32), ("seq", "u16", 52)])

    def test_the_fixed_area_rounds_to_eight(self):
        self.assertEqual(wire.shape(Mixed).size, 56)

    def test_inline_bytes_align_to_one(self):
        @dataclass
        class Id:
            n: zip.u32
            key: Annotated[bytes, zip.fixed(32)]
            after: zip.u64

        # 32 inline bytes follow a u32 at 4, not at 32: they are bytes, not a slot
        # whose alignment is its width.
        self.assertEqual([(s.name, s.offset) for s in wire.shape(Id).slots],
                         [("n", 0), ("key", 4), ("after", 40)])

    def test_a_plain_int_is_i64_and_a_plain_float_is_f64(self):
        @dataclass
        class Plain:
            n: int
            x: float

        self.assertEqual([s.type for s in wire.shape(Plain).slots], ["i64", "f64"])

    def test_optional_is_the_same_slot_and_says_it_may_be_absent(self):
        @dataclass
        class Maybe:
            here: str
            gone: Optional[str]

        slots = wire.shape(Maybe).slots
        self.assertEqual([(s.type, s.offset) for s in slots], [("text", 0), ("text", 8)])
        self.assertEqual([s.opt for s in slots], [False, True])

    def test_annotated_and_optional_peel_in_either_order(self):
        @dataclass
        class Either:
            a: Annotated[Optional[str], "written outside"]
            b: Optional[Annotated[str, "written inside"]]

        slots = wire.shape(Either).slots
        self.assertEqual([s.doc for s in slots], ["written outside", "written inside"])
        self.assertTrue(all(s.opt for s in slots))

    def test_a_nested_declaration_crosses_as_bytes_and_keeps_its_own_layout(self):
        @dataclass
        class Inner:
            n: zip.u32

        @dataclass
        class Outer:
            one: Inner
            many: list[Inner]

        slots = wire.shape(Outer).slots
        self.assertEqual([(s.type, s.offset, s.width) for s in slots],
                         [("bytes", 0, 8), ("list<bytes>", 8, 8)])
        self.assertEqual([s.holds for s in slots], [Inner, Inner])
        self.assertEqual([s.holds_list for s in slots], [False, True])

    def test_a_list_of_bytes_is_a_list_of_the_variable_kind(self):
        @dataclass
        class Blobs:
            parts: list[bytes]

        self.assertEqual(wire.shape(Blobs).slots[0].type, "list<bytes>")


@dataclass
class Loop:
    """A declaration that reaches itself: an offset and a width cannot say how
    big it is."""

    next: "Loop"


class Refusals(unittest.TestCase):
    """A field that cannot be a slot is named, with a cause, and the whole list
    is answered — not the first one."""

    def refuse(self, ann):
        @dataclass
        class Holder:
            f: ann

        sh = wire.shape(Holder)
        self.assertEqual(len(sh.refusals), 1, sh)
        return sh.refusals[0]

    def test_any_names_no_type(self):
        self.assertEqual(self.refuse(Any).cause, wire.ANY)

    def test_a_list_of_nothing_in_particular_names_no_type(self):
        self.assertEqual(self.refuse(list).cause, wire.ANY)

    def test_a_map_is_a_key_set_not_a_layout(self):
        self.assertEqual(self.refuse(dict[str, int]).cause, wire.MAP)

    def test_a_set_a_tuple_and_a_union_have_no_wire_form(self):
        for ann in (set[int], tuple[int, str], Union[int, str], complex):
            with self.subTest(ann=ann):
                self.assertEqual(self.refuse(ann).cause, wire.UNWIRABLE)

    def test_a_list_of_lists_has_no_wire_form(self):
        self.assertEqual(self.refuse(list[list[int]]).cause, wire.UNWIRABLE)

    def test_a_declaration_that_reaches_a_refusal_is_refused(self):
        @dataclass
        class Bad:
            whatever: Any

        @dataclass
        class Holds:
            it: Bad

        r = wire.shape(Holds).refusals
        self.assertEqual([(x.field, x.cause) for x in r], [("Holds.it", wire.REACHES)])

    def test_a_declaration_that_reaches_itself_has_no_fixed_size(self):
        self.assertEqual([r.cause for r in wire.shape(Loop).refusals], [wire.REACHES])

    def test_every_bad_field_is_named_not_only_the_first(self):
        @dataclass
        class Three:
            a: Any
            b: dict[str, int]
            ok: str
            c: set[int]

        r = wire.shape(Three).refusals
        self.assertEqual([(x.field.split(".")[1], x.cause) for x in r],
                         [("a", wire.ANY), ("b", wire.MAP), ("c", wire.UNWIRABLE)])
        self.assertEqual([s.name for s in wire.shape(Three).slots], ["ok"])

    def test_a_nested_declaration_with_nothing_in_it_crosses_empty(self):
        @dataclass
        class Nothing:
            pass

        @dataclass
        class Carries:
            n: Nothing

        sh = wire.shape(Carries)
        self.assertEqual(sh.refusals, ())
        self.assertEqual([s.name for s in sh.hollow], ["n"])


if __name__ == "__main__":
    unittest.main()
