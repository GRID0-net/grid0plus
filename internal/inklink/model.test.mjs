import test from 'node:test';import assert from 'node:assert/strict';
import fs from'node:fs';
const src=fs.readFileSync(new URL('./assets/model.js',import.meta.url),'utf8');
const {battle,job,battleSummary,grade,averageWaves,shiftDate,bossName}=await import('data:text/javascript;base64,'+Buffer.from(src).toString('base64'));
test('battle stats select the signed-in participant, not the first player',()=>{let r=battle({id:'a',fields:{stage:21,personal_data_team1:[{npln_user_id:'other',result:{paint_point:100}},{npln_user_id:'self',profile:{player_info:{weapon_id:1010}},result:{paint_point:900,deemed_result:1}}]}},'self');assert.equal(r.points,900);assert.equal(r.outcome,'Defeat');assert.equal(r.weapon,1010);assert.equal(r.duration,null)});
test('missing timing is not a zero performance average',()=>assert.equal(battleSummary([{outcome:'Victory',duration:null,kills:0,deaths:0,specials:0}]).kills,null));
test('grade titles display their gauge instead of the total coordinate',()=>{assert.equal(grade(8,810),'Eggsecutive VP 10');assert.equal(grade(8,1799),'Eggsecutive VP 999');assert.equal(grade(2,260),'Go-Getter 60')});
test('job rows use the signed-in player and include their eggs and real delta',()=>{const j=job({id:'j',fields:{result:0,personal_data:[{npln_user_id:'other',grade_point_after:900},{npln_user_id:'self',grade_after:8,grade_point_before:810,grade_point_after:830,personal_wave_result:[{golden_ikura_num:5,ikura_num:50,weapon_id:1010}]}]}},'self');assert.equal(j.after,830);assert.equal(j.arrow,'↑');assert.equal(j.golden,5);assert.equal(j.clear,true)});

test('completed waves count a successful third wave, not the zero-based wave index',()=>{assert.equal(averageWaves([{finalWave:2,clear:true},{finalWave:1,clear:false}]),2);assert.equal(averageWaves([]),null)});
test('game shift IDs are compact UTC timestamps, not operator IDs',()=>assert.equal(shiftDate('20261014080000'),'2026-10-14T08:00:00.000Z'));

test('King Salmonids use their real names, not protocol identifiers',()=>{assert.equal(bossName('SakeJaw'),'Cohozuna');assert.equal(bossName('SakeRope'),'Horrorboros');assert.equal(bossName('SakelienGiant'),'Megalodontia');assert.equal(bossName('unexpected'),'King Salmonid')});
