export namespace main {
	
	export class BarRow {
	    label: string;
	    tooltip: string;
	    percent: number;
	    hasPercent: boolean;
	    fillRatio: number;
	    countLabel: string;
	    bytes: number;
	    bytesLabel: string;
	
	    static createFrom(source: any = {}) {
	        return new BarRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.tooltip = source["tooltip"];
	        this.percent = source["percent"];
	        this.hasPercent = source["hasPercent"];
	        this.fillRatio = source["fillRatio"];
	        this.countLabel = source["countLabel"];
	        this.bytes = source["bytes"];
	        this.bytesLabel = source["bytesLabel"];
	    }
	}
	export class Card {
	    label: string;
	    tooltip: string;
	    detail: string;
	    sub: string;
	
	    static createFrom(source: any = {}) {
	        return new Card(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.tooltip = source["tooltip"];
	        this.detail = source["detail"];
	        this.sub = source["sub"];
	    }
	}
	export class Overview {
	    heroBytes: number;
	    heroTotalBytes: number;
	    heroTotalBytesLabel: string;
	    heroPercent: number;
	    heroHeadline: string;
	    heroUsageDetail: string;
	    heroUsageSub: string;
	    hasTranscriptData: boolean;
	    hasActivity: boolean;
	    bytesSavedCard: Card;
	    tokensSavedCard: Card;
	    dollarSavedCard: Card;
	    bars: BarRow[];
	    projectCount: number;
	    sessionCount: number;
	
	    static createFrom(source: any = {}) {
	        return new Overview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.heroBytes = source["heroBytes"];
	        this.heroTotalBytes = source["heroTotalBytes"];
	        this.heroTotalBytesLabel = source["heroTotalBytesLabel"];
	        this.heroPercent = source["heroPercent"];
	        this.heroHeadline = source["heroHeadline"];
	        this.heroUsageDetail = source["heroUsageDetail"];
	        this.heroUsageSub = source["heroUsageSub"];
	        this.hasTranscriptData = source["hasTranscriptData"];
	        this.hasActivity = source["hasActivity"];
	        this.bytesSavedCard = this.convertValues(source["bytesSavedCard"], Card);
	        this.tokensSavedCard = this.convertValues(source["tokensSavedCard"], Card);
	        this.dollarSavedCard = this.convertValues(source["dollarSavedCard"], Card);
	        this.bars = this.convertValues(source["bars"], BarRow);
	        this.projectCount = source["projectCount"];
	        this.sessionCount = source["sessionCount"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProjectRow {
	    name: string;
	    path: string;
	    installed: boolean;
	    overview: Overview;
	
	    static createFrom(source: any = {}) {
	        return new ProjectRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.installed = source["installed"];
	        this.overview = this.convertValues(source["overview"], Overview);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionRow {
	    sessionId: string;
	    overview: Overview;
	
	    static createFrom(source: any = {}) {
	        return new SessionRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.overview = this.convertValues(source["overview"], Overview);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Toast {
	    active: boolean;
	    title: string;
	    body: string;
	
	    static createFrom(source: any = {}) {
	        return new Toast(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.title = source["title"];
	        this.body = source["body"];
	    }
	}

}

