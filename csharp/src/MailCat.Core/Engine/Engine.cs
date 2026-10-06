using System.Text;
using MailCat.Categories;
using MailCat.Classification;
using MailCat.Config;
using MailCat.Features;
using MailCat.Learning;
using MailCat.Questions;
using MailCat.Similarity;
using MailCat.Storage;

namespace MailCat.Engine;

/// <summary>
/// Wires the independent components (extraction, classification, question selection,
/// learning, storage) into the interactive workflow: Process → (NextQuestion → Answer)* →
/// Feedback. It owns the session state machine of §30 and the decision rules of §15 and §41.
/// </summary>
public sealed partial class MailCatEngine
{
    readonly Database db;
    readonly MailCatConfig cfg;
    readonly BuiltConfig built;
    readonly CategoryTree tree;
    readonly IFeatureExtractor extractor;
    readonly Classifier cls;
    readonly QuestionSelector sel;
    Learner? learner;
    readonly Func<DateTime> now;
    readonly SimCache sim; // shared by views created with WithoutLearning

    sealed class SimCache
    {
        public ulong Version;
        public IIndex? Index;
        public bool Initialized;
    }

    /// <summary>Builds an engine over a database and a validated configuration. It synchronizes categories and configured questions into the database.</summary>
    public MailCatEngine(Database db, BuiltConfig built, Func<DateTime>? clock = null)
    {
        this.db = db;
        this.built = built;
        cfg = built.Config;
        tree = built.Tree;
        now = clock ?? (() => DateTime.UtcNow);
        sim = new SimCache();

        db.SyncCategories(built.Categories);
        SyncQuestions();

        extractor = Extractors.Default(db);
        var signals = new List<ISignal> { built.Rules, new LearnedSignal(db, tree.Path) };
        if (cfg.Similarity.Enabled && cfg.Privacy.StoreBody)
        {
            var s = cfg.Similarity;
            signals.Add(new SimilaritySignal
            {
                Load = LoadIndex, K = s.K, MinSim = s.MinSimilarity, MaxWeight = s.MaxWeight, Name = tree.Path,
            });
        }
        cls = new Classifier(tree, signals, cfg.ClassifierThresholds());
        var p = new SelectorParams();
        if (cfg.QuestionSelection.MinGain > 0) p.MinGain = cfg.QuestionSelection.MinGain;
        sel = new QuestionSelector(tree, db, p);
        ReloadQuestions();
        if (cfg.Learning.Enabled) learner = new Learner(db, tree, now);
    }

    /// <summary>Stores the configured questions and disables stored config-sourced ones that disappeared from the configuration.</summary>
    void SyncQuestions()
    {
        var inConfig = new HashSet<string>(StringComparer.Ordinal);
        foreach (var q in built.Questions)
        {
            inConfig.Add(q.Id);
            db.SaveQuestion(q, false);
        }
        foreach (var q in db.Questions())
        {
            if (q.Source == QuestionSource.Config && !inConfig.Contains(q.Id) && !q.Disabled)
                db.SetQuestionState(q.Id, true, false);
        }
    }

    /// <summary>Refreshes the selector's catalogue from the database.</summary>
    public void ReloadQuestions() => sel.Questions = db.Questions();

    /// <summary>A view of the engine that never updates learned rules. Used by offline evaluation on held-out data (§35).</summary>
    public MailCatEngine WithoutLearning()
    {
        var c = (MailCatEngine)MemberwiseClone();
        c.learner = null;
        return c;
    }

    public CategoryTree Tree => tree;
    public Database Db => db;
    public MailCatConfig Config => cfg;
    public IReadOnlyList<Question> Questions => sel.Questions;

    IIndex? LoadIndex()
    {
        var c = sim;
        ulong v = db.LabelVersion;
        if (c.Initialized && c.Version == v) return c.Index;
        c.Index = new TfIdfIndex(db.LabeledDocs(4000));
        c.Version = v;
        c.Initialized = true;
        return c.Index;
    }

    /// <summary>FNV-1a 32-bit hash.</summary>
    internal static uint Fnv1a(string s) => Fnv1aPublic(s);

    /// <summary>FNV-1a 32-bit hash (shared with the evaluation splitter).</summary>
    public static uint Fnv1aPublic(string s)
    {
        uint h = 2166136261;
        foreach (var b in Encoding.UTF8.GetBytes(s))
        {
            h ^= b;
            h *= 16777619;
        }
        return h;
    }

    /// <summary>
    /// Samples automatic classifications for a "was this right?" prompt (§16). Deterministic
    /// per email; lower confidence is sampled more.
    /// </summary>
    bool ShouldConfirm(string emailId, double score)
    {
        double bs = cfg.Confirmation.BaseRate;
        if (bs <= 0) return false;
        double high = cfg.Thresholds.Auto;
        double p = bs;
        if (high < 1) p = bs * (2 - (score - high) / (1 - high)); // base..2*base as score goes 1..high
        return Fnv1a(cfg.Confirmation.Seed + "|" + emailId) / (double)(1UL << 32) < p;
    }
}
