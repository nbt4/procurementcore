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
 return <div className="content"><div className="page-header"><div><p className="eyebrow">Einkaufssteuerung</p><h2>Guten Einkauf im Blick.</h2><p>Von der Bedarfsmeldung bis zum Wareneingang – zentral und nachvollziehbar.</p></div></div>
 <div className="grid metrics">{metrics.map(({label,value,icon:Icon})=><article className="metric" key={label}><div className="metric-icon"><Icon size={18}/></div><div className="metric-value">{value}</div><div className="metric-label">{label}</div></article>)}</div>
 <div className="grid" style={{gridTemplateColumns:'repeat(2,minmax(0,1fr))',marginTop:'1rem'}}><article className="metric"><div className="metric-icon"><Coins size={18}/></div><div className="metric-value">{euro(data?.spend.cents)}</div><div className="metric-label">Bestellvolumen gesamt</div></article><article className="metric"><div className="metric-icon"><CheckCircle2 size={18}/></div><div className="metric-value">{euro(data?.savings.cents)}</div><div className="metric-label">Einsparung gegenüber Bedarfsschätzung</div></article></div>
 <div className="grid dashboard-lower"><section className="panel"><header className="panel-header"><h3>Procure-to-Receive</h3><span className="badge pink">Durchgängiger Workflow</span></header><div className="panel-body workflow">{[[ClipboardCheck,'Bedarf','Erfassen'],[FileCheck2,'Freigabe','Prüfen'],[Building2,'Sourcing','Vergleichen'],[ShoppingCart,'Bestellung','Auslösen'],[PackageCheck,'Eingang','Verbuchen']].map(([I,title,sub],i)=>{const Icon=I as typeof ClipboardCheck;return <div className="workflow-step" key={title as string}><Icon size={18}/><strong>{i+1}. {title as string}</strong><span>{sub as string}</span></div>})}</div></section>
 <section className="panel"><header className="panel-header"><h3>Letzte Aktivitäten</h3></header><div className="panel-body activity-list">{data?.recentActivity.length?data.recentActivity.map(row=><div className="activity" key={row.id}><span className="activity-dot"/><div><strong>{row.action.replaceAll('_',' ')} · {row.username}</strong><p>{row.entityType} #{row.entityId} · {dateTime(row.createdAt)}</p></div></div>):<p style={{color:'var(--text-muted)',fontSize:'.8rem'}}>Noch keine Aktivitäten.</p>}</div></section></div></div>
}
