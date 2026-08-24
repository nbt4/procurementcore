import { describe, expect, it } from 'vitest'
import type { Product } from '../lib/types'
import { sortProducts } from './CatalogPage'

const product=(id:number,name:string,manufacturer:string,prices:number[]=[]):Product=>({
  id,
  sku:`SKU-${id}`,
  name,
  manufacturer,
  offers:prices.map((priceCents,index)=>({id:id*10+index,priceCents})),
} as Product)

const rows=[
  product(1,'Mikrofon','Shure',[10900,11900]),
  product(2,'Kabel','Adam Hall',[629]),
  product(3,'Stativ','K&M'),
]

describe('sortProducts',()=>{
  it('sortiert Hersteller auf- und absteigend',()=>{
    expect(sortProducts(rows,{key:'manufacturer',direction:'asc'}).map(row=>row.manufacturer)).toEqual(['Adam Hall','K&M','Shure'])
    expect(sortProducts(rows,{key:'manufacturer',direction:'desc'}).map(row=>row.manufacturer)).toEqual(['Shure','K&M','Adam Hall'])
  })

  it('sortiert nach dem günstigsten Angebot und hält fehlende Preise am Ende',()=>{
    expect(sortProducts(rows,{key:'price',direction:'asc'}).map(row=>row.name)).toEqual(['Kabel','Mikrofon','Stativ'])
    expect(sortProducts(rows,{key:'price',direction:'desc'}).map(row=>row.name)).toEqual(['Mikrofon','Kabel','Stativ'])
  })

  it('sortiert nach der Anzahl der Bezugsquellen',()=>{
    expect(sortProducts(rows,{key:'offers',direction:'desc'}).map(row=>row.name)).toEqual(['Mikrofon','Kabel','Stativ'])
  })
})
