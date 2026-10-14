"""Regenerates samples/deck.pptx (python-pptx 1.0.2): a title slide, bullets,
a table, a bar chart and a drawn shape -- what agents' decks usually hold.

    python3 -m venv /tmp/v && /tmp/v/bin/pip install python-pptx==1.0.2
    /tmp/v/bin/python examples/previewers/pptx/samples/generate.py
"""

import datetime
import pathlib

from pptx import Presentation
from pptx.chart.data import CategoryChartData
from pptx.dml.color import RGBColor
from pptx.enum.chart import XL_CHART_TYPE
from pptx.enum.shapes import MSO_SHAPE
from pptx.util import Inches, Pt

prs = Presentation()
# The default 4:3 template: its layouts place the placeholders.

s = prs.slides.add_slide(prs.slide_layouts[0])
s.shapes.title.text = "Quarterly review"
s.placeholders[1].text = "Sample deck for the PowerPoint previewer"

s = prs.slides.add_slide(prs.slide_layouts[1])
s.shapes.title.text = "Highlights"
body = s.placeholders[1].text_frame
body.text = "Revenue up 18% year over year"
for line in ("Two new regions launched", "Support backlog halved"):
    body.add_paragraph().text = line

s = prs.slides.add_slide(prs.slide_layouts[5])
s.shapes.title.text = "By region"
rows = [("Region", "Q1", "Q2"), ("North", "120", "140"), ("South", "95", "130"), ("West", "80", "88")]
table = s.shapes.add_table(len(rows), 3, Inches(1), Inches(2), Inches(8), Inches(2.5)).table
for r, row in enumerate(rows):
    for c, value in enumerate(row):
        table.cell(r, c).text = value

s = prs.slides.add_slide(prs.slide_layouts[5])
s.shapes.title.text = "Growth"
data = CategoryChartData()
data.categories = ["North", "South", "West"]
data.add_series("Q1", (120, 95, 80))
data.add_series("Q2", (140, 130, 88))
s.shapes.add_chart(XL_CHART_TYPE.COLUMN_CLUSTERED, Inches(1), Inches(1.8), Inches(8), Inches(5), data)

s = prs.slides.add_slide(prs.slide_layouts[6])
box = s.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Inches(2.5), Inches(2.5), Inches(5), Inches(2.5))
box.fill.solid()
box.fill.fore_color.rgb = RGBColor(0x0A, 0x74, 0xD9)
box.text_frame.text = "Thank you"
box.text_frame.paragraphs[0].runs[0].font.size = Pt(40)

# A fixed timestamp keeps the file stable across regenerations.
fixed = datetime.datetime(2026, 10, 14)
prs.core_properties.created = prs.core_properties.modified = fixed
prs.core_properties.last_printed = fixed
# The default template names its own author; a sample names no one.
prs.core_properties.last_modified_by = ""
prs.save(pathlib.Path(__file__).with_name("deck.pptx"))
