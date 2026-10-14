// Read-only presentation of the fields Grid0 actually persists. Missing is not zero.
export const number=v=>typeof v==='number'&&Number.isFinite(v)?v:null;
export const list=v=>Array.isArray(v)?v:[];
export const sum=(xs,k)=>xs.reduce((n,x)=>n+(number(x?.[k])??0),0);
export const grade=(title,points)=>{const names=['Apprentice','Part-Timer','Go-Getter','Overachiever','Profreshional Part-Timer','Profreshional +1','Profreshional +2','Profreshional +3','Eggsecutive VP'];return title==null||points==null?null:`${names[title]??'Grade '+title} ${points-title*100}`};
export function battle(r,uid){
 const f=r.fields||{};let p=null;
 for(const k of ['personal_data_team1','personal_data_team2','personal_data_team3'])p||=list(f[k]).find(x=>x.npln_user_id===uid);
 const result=p?.result||{};const info=p?.profile?.player_info||{};
 const weapon=number(info.weapon_id)??number(info.weapon)??number(info.weapon_info?.weapon_id)??number(p?.weapon_id);
 let outcome=({0:'Victory',1:'Defeat',2:'Draw'})[result.deemed_result]||'Unknown';
 if(outcome==='Unknown'&&number(f.winner)>0&&number(p?.team)>0)outcome=f.winner===p.team?'Victory':'Defeat';
 if(p?.disconnected||result.disconnected)outcome='Draw';
 return{id:r.id,time:f.started_at||r.time,stage:number(f.stage),weapon,mode:f.mode??f.game_mode??'',rule:f.game_rule,outcome,points:number(result.paint_point),kills:number(result.kill_num),assists:number(result.kill_assist_num),deaths:number(result.death_num),specials:number(result.special_num),duration:number(f.elapsed_time),raw:f,personal:p};
}
export function job(r,uid){
 const f=r.fields||{};const p=list(f.personal_data).find(x=>x.npln_user_id===uid)||{};const waves=list(p.personal_wave_result);
 const before=number(p.grade_point_before),after=number(p.grade_point_after);
 return{id:r.id,time:f.started_at||r.time,shift:f.shift_id||f.shift_time||'Unrecorded shift',kind:jobKind(f.scenario?.job_type_string),stage:number(f.scenario?.stage),clear:f.result===0?true:f.result===1?false:null,titleBefore:number(p.grade_before),titleAfter:number(p.grade_after),before,after,arrow:before==null||after==null?'':after>before?'↑':after<before?'↓':'→',golden:waves.some(x=>number(x.golden_ikura_num)!=null)?sum(waves,'golden_ikura_num'):null,power:waves.some(x=>number(x.ikura_num)!=null)?sum(waves,'ikura_num'):null,suppliedWeapons:list(f.scenario?.main_weapons).filter(x=>number(x)!==null).slice(0,4),weaponIds:[...new Set(waves.map(x=>number(x.weapon_id)).filter(x=>x!==null))],waves,finalWave:number(f.final_wave),points:number(p.kuma_point),raw:f};
}
export function battleSummary(rows){const samples=rows.filter(x=>x.duration>0&&x.kills!==null&&x.deaths!==null&&x.specials!==null);const seconds=samples.reduce((n,x)=>n+x.duration,0);const rate=k=>seconds?samples.reduce((n,x)=>n+(x[k]??0),0)*60/seconds:null;return{wins:rows.filter(x=>x.outcome==='Victory').length,losses:rows.filter(x=>x.outcome==='Defeat').length,draws:rows.filter(x=>x.outcome==='Draw').length,kills:rate('kills'),assists:rate('assists'),deaths:rate('deaths'),specials:rate('specials'),samples:samples.length}}
export const modeName=mode=>{const m=String(mode).toLowerCase();return m.includes('bankara_match_challenge')?'Anarchy Battle · Series':m.includes('bankara')?'Anarchy Battle · Open':m.includes('x_match')?'X Battle':m.includes('fest')?'Splatfest Battle':m.includes('league')||m.includes('challenge')?'Challenge':m.includes('private')?'Private Battle':m.includes('regular')?'Regular Battle':mode!==''&&mode!=null?`Battle mode ${mode}`:'Mode not recorded'};
export const family=mode=>{const m=modeName(mode);return m.startsWith('Anarchy')?'anarchy':m.startsWith('X ')?'x':m.startsWith('Splatfest')?'fest':m==='Challenge'?'challenge':m==='Private Battle'?'private':m==='Regular Battle'?'regular':'other'};

export function averageWaves(rows){const known=rows.filter(x=>x.finalWave!==null&&x.clear!==null);return known.length?known.reduce((n,x)=>n+Math.min(3,x.finalWave+(x.clear?1:0)),0)/known.length:null}
export function shiftDate(value){const m=/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(String(value));return m?new Date(Date.UTC(...[Number(m[1]),Number(m[2])-1,...m.slice(3).map(Number)])).toISOString():null}

// Protocol codenames map to the localized English names, not similar enemy IDs.
export function bossName(code){return ({SakeJaw:'Cohozuna',SakeRope:'Horrorboros',SakelienGiant:'Megalodontia',Triple:'Triumvirate'})[code] || (code ? 'King Salmonid' : 'No King Salmonid announced')}

export function jobKind(code){return ({Normal:'normal',Regular:'normal',normal:'normal',BigRun:'big_run',big_run:'big_run',TeamContest:'team_contest',Limited:'team_contest',team_contest:'team_contest'})[code]||'unknown'}
export function jobLabel(kind){return ({normal:'Normal Job',big_run:'Big Run',team_contest:'Eggstra Work'})[kind]||'Job type not recorded'}
export function pointCardFor(records,shift,kind){const collection=({normal:'PointCardRegular',big_run:'PointCardBigRun',team_contest:'PointCardLimited'})[kind];return collection?list(records[collection]).find(c=>String(c.id)===String(shift).split('-')[0])?.fields||{}:{}}
