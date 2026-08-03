export namespace main {
	
	export class Card {
	    label: string;
	    count: number;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new Card(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.count = source["count"];
	        this.detail = source["detail"];
	    }
	}
	export class Overview {
	    heroBytes: number;
	    heroHeadline: string;
	    heroSubtext: string;
	    dedup: Card;
	    budgetTrims: Card;
	    retired: Card;
	    projectCount: number;
	    sessionCount: number;
	
	    static createFrom(source: any = {}) {
	        return new Overview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.heroBytes = source["heroBytes"];
	        this.heroHeadline = source["heroHeadline"];
	        this.heroSubtext = source["heroSubtext"];
	        this.dedup = this.convertValues(source["dedup"], Card);
	        this.budgetTrims = this.convertValues(source["budgetTrims"], Card);
	        this.retired = this.convertValues(source["retired"], Card);
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
	    window: Overview;
	
	    static createFrom(source: any = {}) {
	        return new ProjectRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.installed = source["installed"];
	        this.overview = this.convertValues(source["overview"], Overview);
	        this.window = this.convertValues(source["window"], Overview);
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
	export class Settings {
	    quiet: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.quiet = source["quiet"];
	    }
	}

}

