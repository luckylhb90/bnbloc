"""Regenerates samples/doc.docx (python-docx 1.2.0): headings, a styled
paragraph, a bulleted list, a table, a page break and a footer -- what
agents' reports usually hold.

    python3 -m venv /tmp/v && /tmp/v/bin/pip install python-docx==1.2.0
    /tmp/v/bin/python examples/previewers/docx/samples/generate.py
"""

import datetime
import pathlib

from docx import Document
from docx.enum.text import WD_BREAK
from docx.shared import Pt, RGBColor

doc = Document()
doc.add_heading("Quarterly review", 0)
p = doc.add_paragraph("Revenue grew ")
run = p.add_run("18% year over year")
run.bold = True
run.font.color.rgb = RGBColor(0x0A, 0x74, 0xD9)
p.add_run(", led by two new regions.")
doc.add_heading("Highlights", 1)
for line in ("Two new regions launched", "Support backlog halved", "Churn down to 2.1%"):
    doc.add_paragraph(line, style="List Bullet")
doc.add_heading("By region", 1)
rows = [("Region", "Q1", "Q2"), ("North", "120", "140"), ("South", "95", "130"), ("West", "80", "88")]
table = doc.add_table(rows=len(rows), cols=3)
table.style = "Light Grid Accent 1"
for r, row in enumerate(rows):
    for c, value in enumerate(row):
        table.cell(r, c).text = value
doc.add_paragraph().add_run().add_break(WD_BREAK.PAGE)
doc.add_heading("Next steps", 1)
note = doc.add_paragraph("Pilot the new settlement path with three partners in Q4.")
note.runs[0].font.size = Pt(13)
doc.sections[0].footer.paragraphs[0].text = "Sample document for the Word previewer"

fixed = datetime.datetime(2026, 10, 14)
props = doc.core_properties
props.created = props.modified = props.last_printed = fixed
props.author = props.last_modified_by = ""
doc.save(pathlib.Path(__file__).with_name("doc.docx"))
