// node --test examples/previewers/lib/ooxml.test.mjs

import assert from "node:assert/strict";
import { test } from "node:test";

import { attributes, decodeXml, headline, paragraphs, relationships, relsPath, runText } from "./ooxml.ts";

test("XML text and attributes decode entities", () => {
  assert.equal(decodeXml("A &amp; B &lt;3 &#x2014; &#169; &bogus;"), "A & B <3 — © &bogus;");
  assert.deepEqual(attributes('<p:sldId id="256" r:id="rId2"/>'), { id: "256", "r:id": "rId2" });
});

test("relationships resolve against the part's folder", () => {
  const rels = '<Relationships><Relationship Id="rId1" Type="x/slide" Target="slides/slide1.xml"/><Relationship Id="rId2" Type="x/notesSlide" Target="../notesSlides/notesSlide1.xml"/><Relationship Id="rId3" Type="x/hyperlink" Target="https://x" TargetMode="External"/></Relationships>';
  assert.equal(relsPath("ppt/slides/slide1.xml"), "ppt/slides/_rels/slide1.xml.rels");
  const fromPres = relationships(rels, "ppt/presentation.xml");
  assert.equal(fromPres.get("rId1").target, "ppt/slides/slide1.xml");
  const fromSlide = relationships(rels, "ppt/slides/slide1.xml");
  assert.equal(fromSlide.get("rId2").target, "ppt/notesSlides/notesSlide1.xml");
  assert.equal(fromSlide.has("rId3"), false, "external targets are not parts");
  assert.equal(relationships(null, "x").size, 0);
});

test("paragraphs: runs, tabs and breaks; fields left out; empty kept", () => {
  const xml = '<a:p><a:r><a:t>Q3 &amp; Q4</a:t></a:r><a:r><a:tab/><a:t>up</a:t></a:r></a:p><a:p/><a:p><a:fld type="slidenum"><a:t>2</a:t></a:fld></a:p><a:p><a:t>line</a:t><a:br/><a:t>two</a:t></a:p>';
  assert.deepEqual(paragraphs(xml, "a"), ["Q3 & Q4\tup", "", "", "line\ntwo"]);
  assert.equal(runText('<w:t xml:space="preserve"> a </w:t>', "w"), " a ");
  assert.equal(headline("A very long first line that goes on\nsecond", 10), "A very lo…");
});
