#!/usr/bin/env python3
"""Build public name/asset-ID facts; never copies image files or game dumps.

Usage: python3 scripts/build-inklink-catalog.py /path/to/Leanny-splat3
The input remains operator-local. Artwork stays on the named external archive.
"""
import json,sys
from pathlib import Path
source=Path(sys.argv[1]); data=source/'data/mush/1100'
lang=json.loads((source/'data/language/USen.json').read_text())
revision='7280ff9cde8bb1c5dcef46c700c326471584d2e6'
base=f'https://cdn.jsdelivr.net/gh/Leanny/splat3@{revision}/images/'
def rows(name):return json.loads((data/(name+'.json')).read_text())
def catalog(name,folder,names,keyprefix=''):
 out={}
 for row in rows(name):
  code=row['__RowId'];file=keyprefix+code+'.png'
  if not (source/'images'/folder/file).is_file():continue
  name=names.get(code,names.get(code.split('_',1)[-1],code))
  out[str(row['Id'])]={'id':row['Id'],'name':name,'image':base+folder+'/'+file,'code':code}
 return out
vs={}
for row in rows('VersusSceneInfo'):
 code=row['__RowId'];stem=code.removeprefix('Vss_').rstrip('0123456789');file='Vss_'+stem+'.png'
 if (source/'images/stageBanner'/file).is_file():vs[str(row['Id'])]={'id':row['Id'],'name':lang['CommonMsg/VS/VSStageName'].get(stem,stem),'image':base+'stageBanner/'+file}
coop={}
for row in rows('CoopSceneInfo'):
 code=row['__RowId'];stem=code.removeprefix('Cop_');file=code+'.png'
 if not (source/'images/stageBanner'/file).is_file():file='Vss_'+stem+'.png'
 if (source/'images/stageBanner'/file).is_file():coop[str(row['Id'])]={'id':row['Id'],'name':lang['CommonMsg/Coop/CoopStageName'].get(stem,stem),'image':base+'stageBanner/'+file}
weapons=catalog('WeaponInfoMain','weapon_flat',lang['CommonMsg/Weapon/WeaponName_Main'],'Path_Wst_')
# Coop-only IDs use their own row IDs; use the shared model when its flat icon
# is archived under the ordinary weapon's row name.
for row in rows('WeaponInfoMain'):
 key=str(row['Id']);code=row['__RowId']
 if key in weapons or not code.endswith('_Coop'):continue
 ordinary=code.removesuffix('_Coop')+'_00';file='Path_Wst_'+ordinary+'.png'
 if (source/'images/weapon_flat'/file).is_file():weapons[key]={'id':row['Id'],'name':lang['CommonMsg/Weapon/WeaponName_Main'].get(code,ordinary),'image':base+'weapon_flat/'+file,'code':code}
gears={}
for kind,table,suffix in [('head','GearInfoHead','Head'),('clothes','GearInfoClothes','Clothes'),('shoes','GearInfoShoes','Shoes')]:
 gears[kind]=catalog(table,'gear',lang['CommonMsg/Gear/GearName_'+suffix])
result={'source':'https://github.com/Leanny/splat3','revision':revision,'base':base,'vs':vs,'coop':coop,'weapons':weapons,'gear':gears}
Path('internal/inklink/assets/catalog.json').write_text(json.dumps(result,separators=(',',':'))+'\n')
print('Catalog:',len(vs),'VS stages,',len(coop),'Coop stages,',len(weapons),'weapons,',sum(map(len,gears.values())),'gear pieces')
