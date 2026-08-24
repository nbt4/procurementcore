import { useEffect, useState } from 'react'
import { BellRing, Boxes, Building2, CheckCircle2, ClipboardCheck, Coins, FileCheck2, PackageCheck, ShoppingCart } from 'lucide-react'
import { api, dateTime, euro } from '../lib/api'
import type { Dashboard } from '../lib/types'
import { useApp } from '../App'

export default function DashboardPage(){
 const {refreshKey}=useApp(),[data,setData]=useState<Dashboard|null>(null)
 useEffect(()=>{api<Dashboard>('/dashboard').then(setData)},[refreshKey])
 const metrics=[
  {label:'Offene Freigaben',value:data?.pendingApprovals??0,icon:ClipboardCheck},
  {label:'Ausgelöste Tiefpreise',value:data?.triggeredAlerts??0,icon:BellRing},
  {label:'Preferred Supplier',value:data?.preferredSuppliers??0,icon:Building2},
  {label:'Aktive Katalogartikel',value:data?.activeProducts??0,icon:Boxes},
 ]
 return <div className="content"><div className="page-header"><div><h2>Einkaufsübersicht</h2><p>Bedarfe, Bezugsquellen und Bestellungen auf einem Stand.</p></div><span className="page-reference">Stand heute</span></div>
 <section className="metrics" aria-label="Einkaufskennzahlen">{metrics.map(({label,value,icon:Icon})=><article className="metric" key={label}><div className="metric-label"><Icon size={15}/>{label}</div><div className="metric-value">{value}</div></article>)}<article className="metric metric-wide"><div className="metric-label"><Coins size={15}/>Bestellvolumen gesamt</div><div className="metric-value">{euro(data?.spend.cents)}</div></article><article className="metric metric-wide"><div className="metric-label"><CheckCircle2 size={15}/>Realisierte Einsparung</div><div className="metric-value">{euro(data?.savings.cents)}</div></article></section>
 <div className="grid dashboard-lower"><section className="panel"><header className="panel-header"><div><h3>Beschaffungsablauf</h3><p>Vom Bedarf bis zum gebuchten Eingang</p></div><span className="panel-reference">5 Schritte</span></header><div className="panel-body workflow">{[[ClipboardCheck,'Bedarf','Erfassen'],[FileCheck2,'Freigabe','Prüfen'],[Building2,'Sourcing','Vergleichen'],[ShoppingCart,'Bestellung','Auslösen'],[PackageCheck,'Eingang','Verbuchen']].map(([I,title,sub],i)=>{const Icon=I as typeof ClipboardCheck;return <div className="workflow-step" key={title as string}><span className="workflow-index">0{i+1}</span><Icon size={17}/><strong>{title as string}</strong><small>{sub as string}</small></div>})}</div></section>
 <section className="panel"><header className="panel-header"><div><h3>Letzte Aktivitäten</h3><p>Änderungen im Einkauf</p></div></header><div className="panel-body activity-list">{data?.recentActivity.length?data.recentActivity.map(row=><div className="activity" key={row.id}><span className="activity-dot"/><div><strong>{row.action.replaceAll('_',' ')} · {row.username}</strong><p>{row.entityType} #{row.entityId} · {dateTime(row.createdAt)}</p></div></div>):<p className="muted-copy">Noch keine Aktivitäten.</p>}</div></section></div></div>
}
