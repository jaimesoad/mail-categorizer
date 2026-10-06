namespace MailCat.Categories;

/// <summary>A node of the category tree.</summary>
public sealed record Category(string Id, string ParentId, string Name, string Description, bool Enabled)
{
    public Category(string id, string name, string parentId = "") : this(id, parentId, name, "", true) { }
}

/// <summary>An immutable view over a validated set of categories.</summary>
public sealed class CategoryTree
{
    /// <summary>Reserved category meaning "not enough evidence" (§32).</summary>
    public const string UnknownId = "unknown";

    readonly Dictionary<string, Category> byId = new(StringComparer.Ordinal);
    readonly Dictionary<string, List<string>> children = new(StringComparer.Ordinal); // "" = roots

    /// <summary>
    /// Validates cats (unique ids, existing parents, no cycles) and builds a tree. The
    /// reserved Unknown category is always added as a root.
    /// </summary>
    public CategoryTree(IEnumerable<Category> cats)
    {
        foreach (var c0 in cats)
        {
            var c = c0;
            if (c.Id == "") throw new ConfigException("category with empty id");
            if (c.Id == UnknownId && c.ParentId != "")
                throw new ConfigException($"category \"{UnknownId}\" is reserved and cannot have a parent");
            if (byId.ContainsKey(c.Id)) throw new ConfigException($"duplicate category id \"{c.Id}\"");
            if (c.Name == "") c = c with { Name = c.Id };
            byId[c.Id] = c;
        }
        if (!byId.ContainsKey(UnknownId))
            byId[UnknownId] = new Category(UnknownId, "", "Desconocido", "Sin evidencia suficiente para clasificar", true);

        foreach (var (id, c) in byId.OrderBy(kv => kv.Key, StringComparer.Ordinal))
        {
            if (c.ParentId != "" && !byId.ContainsKey(c.ParentId))
                throw new ConfigException($"category \"{id}\": parent \"{c.ParentId}\" does not exist");
            var seen = new HashSet<string> { id };
            for (var p = c.ParentId; p != ""; p = byId.TryGetValue(p, out var pc) ? pc.ParentId : "")
            {
                if (!seen.Add(p)) throw new ConfigException($"category \"{id}\": cycle in hierarchy");
            }
            if (!children.TryGetValue(c.ParentId, out var l)) children[c.ParentId] = l = [];
            l.Add(id);
        }
        foreach (var l in children.Values) l.Sort(StringComparer.Ordinal);
    }

    public Category? Get(string id) => byId.TryGetValue(id, out var c) ? c : null;

    public bool Has(string id) => byId.ContainsKey(id);

    /// <summary>True when id exists and it and all its ancestors are enabled.</summary>
    public bool Enabled(string id)
    {
        for (var cur = id; cur != ""; cur = byId[cur].ParentId)
        {
            if (!byId.TryGetValue(cur, out var c) || !c.Enabled) return false;
        }
        return true;
    }

    /// <summary>
    /// The enabled children of id ("" for roots), sorted by id. Unknown is never returned:
    /// it is handled as residual mass by the classifier.
    /// </summary>
    public List<string> Children(string id) =>
        children.TryGetValue(id, out var l)
            ? l.Where(c => c != UnknownId && byId[c].Enabled).ToList()
            : [];

    public List<string> Roots() => Children("");

    /// <summary>The ids from the parent of id up to the root.</summary>
    public List<string> Ancestors(string id)
    {
        var o = new List<string>();
        if (!byId.TryGetValue(id, out var c)) return o;
        for (var p = c.ParentId; p != ""; p = byId.TryGetValue(p, out var pc) ? pc.ParentId : "") o.Add(p);
        return o;
    }

    /// <summary>True when anc is a strict ancestor of id.</summary>
    public bool IsAncestor(string anc, string id) => Ancestors(id).Contains(anc);

    /// <summary>The human readable path ("Finance / Bank").</summary>
    public string Path(string id) => byId.ContainsKey(id) ? string.Join(" / ", PathNames(id)) : id;

    /// <summary>The names from the root to id.</summary>
    public List<string> PathNames(string id)
    {
        if (!byId.TryGetValue(id, out var c)) return [id];
        var o = new List<string> { c.Name };
        foreach (var a in Ancestors(id)) o.Insert(0, byId[a].Name);
        return o;
    }

    /// <summary>Every category sorted by id.</summary>
    public List<Category> All() => byId.Values.OrderBy(c => c.Id, StringComparer.Ordinal).ToList();

    /// <summary>Visits categories depth-first in sorted order with their depth.</summary>
    public void Walk(Action<Category, int> fn)
    {
        void Rec(string parent, int depth)
        {
            if (!children.TryGetValue(parent, out var l)) return;
            foreach (var id in l)
            {
                fn(byId[id], depth);
                Rec(id, depth + 1);
            }
        }
        Rec("", 0);
    }
}
