#!/usr/bin/env python3
"""Derive geographic SVG fallback and GeoJSON from a pinned OpenStreetMap export.
No invented borders, runtime API keys, GIS packages or projection approximations.
"""
import json, math, urllib.request
from pathlib import Path
SOURCE='https://raw.githubusercontent.com/yassermzh/iran-map/dae7d65757e6935c3ad12a97056a4480211742d9/dist/tehran_areas.geojson'
root=Path(__file__).resolve().parents[1]
data=json.load(urllib.request.urlopen(SOURCE,timeout=30))
features=sorted(data['features'],key=lambda f:f['properties']['area'])
assert [f['properties']['area'] for f in features]==list(range(1,23))
def project(p):
 lon,lat=p
 return math.radians(lon),math.log(math.tan(math.pi/4+math.radians(lat)/2))
allpoints=[project(p) for f in features for ring in f['geometry']['coordinates'] for p in ring]
x0,y0=min(p[0] for p in allpoints),min(p[1] for p in allpoints)
x1,y1=max(p[0] for p in allpoints),max(p[1] for p in allpoints)
scale=min(960/(x1-x0),610/(y1-y0))
left=(1000-(x1-x0)*scale)/2;top=(650-(y1-y0)*scale)/2
def pixel(p):
 x,y=project(p)
 return [round(left+(x-x0)*scale,3),round(top+(y1-y)*scale,3)]
def inside(p,ring):
 x,y=p; odd=False
 for a,b in zip(ring,ring[1:]+ring[:1]):
  if (a[1]>y)!=(b[1]>y) and x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0]:odd=not odd
 return odd
out=[]
for f in features:
 n=f['properties']['area'];rings=f['geometry']['coordinates'];r=rings[0]
 cross=[a[0]*b[1]-b[0]*a[1] for a,b in zip(r,r[1:]+r[:1])];area=sum(cross)
 centroid=[sum((a[k]+b[k])*c for a,b,c in zip(r,r[1:]+r[:1],cross))/(3*area) for k in [0,1]]
 if not inside(centroid,r) or any(inside(centroid,hole) for hole in rings[1:]):
  # Deterministic interior label, without changing the polygon itself.
  lo=[min(p[k] for p in r) for k in [0,1]];hi=[max(p[k] for p in r) for k in [0,1]]
  candidates=[[lo[0]+(hi[0]-lo[0])*i/20,lo[1]+(hi[1]-lo[1])*j/20] for i in range(1,20) for j in range(1,20)]
  centroid=min((p for p in candidates if inside(p,r) and not any(inside(p,h) for h in rings[1:])),key=lambda p:sum((p[k]-centroid[k])**2 for k in [0,1]))
 x,y=pixel(centroid)
 path=' '.join('M'+' L'.join(','.join(map(str,pixel(p))) for p in ring)+' Z' for ring in rings)
 out.append(dict(ID=f'tehran-{n}',Name=f'منطقهٔ {n} تهران',Number=n,Path=path,X=x,Y=y))
 f['properties']={'id':f'tehran-{n}','number':n,'name':f'منطقهٔ {n} تهران','osm_relation':abs(f['properties']['osm_id']),'label':[centroid[1],centroid[0]]}
(root/'web/static/maps/tehran-districts.geojson').write_text(json.dumps({'type':'FeatureCollection','features':features},ensure_ascii=False,separators=(',',':')))
(root/'config/tehran-district-paths.json').write_text(json.dumps(out,ensure_ascii=False,separators=(',',':')))
print('Exported 22 real districts; labels are inside their respective boundaries.')
