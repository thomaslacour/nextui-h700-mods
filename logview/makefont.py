#!/usr/bin/env python3
"""Build an embedded 8-bit alpha glyph atlas: no font files shipped.

Requires Pillow at build time only. The normal Linux ARM64 build requires Go only.
Font files are read locally from the developer's machine and never distributed.

Usage:
  python3 makefont.py [--ui-bold PATH] [--text PATH]
"""
import argparse
import base64
import math
import zlib
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

P = argparse.ArgumentParser()
P.add_argument('--ui-bold', default='/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf')
P.add_argument('--text', default='/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf')
a = P.parse_args()

# Render the entire ISO western range, common log punctuation, and NextUI arrows.
chars = list(dict.fromkeys(chr(i) for i in range(32, 127)))
chars += list('éèêëàâäçùûüôöîïÉÈÀÇ°•→←↑↓…–—«»’')
chars = list(dict.fromkeys(chars))
faces = [('title',a.ui_bold,24), ('menu',a.ui_bold,20),
         ('body',a.text,13), ('hint',a.ui_bold,18)]

bitmap = bytearray()
def lit(ch):
    if ch == '\\': return "'\\\\'"
    if ch == "'": return "'\\\''"
    if ch in ['\n','\r','\t']: return "'\\" + {'\n':'n','\r':'r','\t':'t'}[ch] + "'"
    if ord(ch) < 127: return "'" + ch + "'"
    return f"'\\u{ord(ch):04x}'"

out = []
out += [
    '// Generated anti-aliased glyph alpha atlas; no font file is embedded.',
    '// Regenerate with: python3 source/makefont.py [--ui-bold LOCAL_TTF --text LOCAL_TTF]',
    'package main',
    '',
    'import ("bytes"; "compress/zlib"; "encoding/base64"; "io")',
    '',
    'type glyphInfo struct { off, width, advance int }',
    'type rasterFont struct { height, lineHeight int; glyphs map[rune]glyphInfo }',
    'var faceTitle, faceMenu, faceBody, faceHint rasterFont',
    'var fontAtlas []byte',
    '',
]
code_faces=[]
for faceName,fontFile,size in faces:
    font = ImageFont.truetype(fontFile, size)
    ascend,descend = font.getmetrics()
    glyph_h = ascend + descend + 1
    face_lines=[]
    for char in chars:
        bbox=font.getbbox(char,anchor='ls')
        # maintain a consistent baseline, and enough room for accent/descenders
        x0,y0,x1,y1=bbox
        offx=max(0,-x0)
        advance=max(1,math.ceil(font.getlength(char)))
        width=max(advance, x1+offx, 1) + 1
        im=Image.new('L',(width,glyph_h),0)
        ImageDraw.Draw(im).text((offx,ascend),char,fill=255,font=font,anchor='ls')
        offset=len(bitmap)
        bitmap.extend(im.tobytes())
        face_lines.append('        %s: {off:%d, width:%d, advance:%d},' % (lit(char),offset,width,advance))
    code_faces += [f'  {faceName}Glyphs := map[rune]glyphInfo{{',*face_lines,'  }',
        f'  face{faceName.capitalize()} = rasterFont{{height:{glyph_h}, lineHeight:{glyph_h},glyphs:{faceName}Glyphs}}']

compressed=base64.b64encode(zlib.compress(bytes(bitmap),9)).decode('ascii')
out += [
    'const fontAtlasZlib =',
]
for i in range(0,len(compressed),100):
    chunk=compressed[i:i+100]
    out.append('    "'+chunk+'"' + (' +' if i+100<len(compressed) else ''))
out += [
    '',
    'func init() {',
    '  b, err := base64.StdEncoding.DecodeString(fontAtlasZlib); if err != nil { panic(err) }',
    '  z, err := zlib.NewReader(bytes.NewReader(b)); if err != nil { panic(err) }',
    '  fontAtlas, err = io.ReadAll(z); _ = z.Close(); if err != nil { panic(err) }',
    *code_faces,
    '}',
]
path=Path(__file__).parent/'font_generated.go'
path.write_text('\n'.join(out)+'\n')
print('faces',len(faces),'chars',len(chars),'atlas decoded bytes',len(bitmap),'compressed',len(compressed),'source',path.stat().st_size)
