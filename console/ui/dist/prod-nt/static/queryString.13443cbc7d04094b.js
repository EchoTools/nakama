"use strict";(self.webpackChunkui=self.webpackChunkui||[]).push([[538],{820:(I,n,t)=>{function d(a,i){return Object.keys(i).reduce(function(s,e){return e.startsWith(a)&&(s[e.substr(a.length)]=i[e]),s},{})}t.r(n),t.d(n,{queryString:()=>y});var c=t(569),m=t(5774);function y(a,i){var s=document.createElement("a");s.href=i;var r=s.search.slice(1).split("&").reduce(function(h,b){var l=b.split("=");return h[l[0]]=(0,c.p)(l[1]),h},{}),u=[],A=r.ajs_uid,_=r.ajs_event,g=r.ajs_aid,v=(0,m.Qd)(a.options.useQueryString)?a.options.useQueryString:{},j=v.aid,P=void 0===j?/.+/:j,o=v.uid,S=void 0===o?/.+/:o;if(g){var f=Array.isArray(r.ajs_aid)?r.ajs_aid[0]:r.ajs_aid;P.test(f)&&a.setAnonymousId(f)}if(A){var p=Array.isArray(r.ajs_uid)?r.ajs_uid[0]:r.ajs_uid;if(S.test(p)){var k=d("ajs_trait_",r);u.push(a.identify(p,k))}}if(_){var C=Array.isArray(r.ajs_event)?r.ajs_event[0]:r.ajs_event,Q=d("ajs_prop_",r);u.push(a.track(C,Q))}return Promise.all(u)}}}]);