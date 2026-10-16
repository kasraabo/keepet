#!/usr/bin/env python3
"""Derive OFL digit-only faces; display Latin/Arabic digits as Persian glyphs.
The original character values (identifiers, URLs, API secrets) remain intact.
Requires fonttools with brotli support. Original license: docs/third-party.md.
"""
from pathlib import Path
from fontTools.ttLib import TTFont
from fontTools import subset
root=Path(__file__).resolve().parent.parent
for weight in ['Regular','Bold']:
    font=TTFont(root/f'web/static/vendor/Vazirmatn-{weight}.woff2')
    source=font.getBestCmap()
    for table in font['cmap'].tables:
        if table.isUnicode():
            for i in range(10):
                table.cmap[ord('0')+i]=source[0x6f0+i]
                table.cmap[0x660+i]=source[0x6f0+i]
    for record in font['name'].names:
        if record.nameID in [1,3,4,6,16]:
            label='KeepetDigits-'+weight if record.nameID==6 else 'Keepet Persian Digits '+weight
            record.string=label.encode(record.getEncoding())
    options=subset.Options();options.flavor='woff2';options.name_IDs=['*'];options.name_legacy=True
    cutter=subset.Subsetter(options=options);cutter.populate(unicodes=list(range(48,58))+list(range(0x660,0x66a))+list(range(0x6f0,0x6fa)));cutter.subset(font)
    font.flavor='woff2';font.save(root/f'web/static/vendor/KeepetDigits-{weight}.woff2')
